/*
* @Author: yangdihang
* @Date: 2020/6/8
 */

package controllers

import (
	"fmt"
	"strings"

	envoy_config_core_v3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	envoy_extensions_wasm_v3 "github.com/envoyproxy/go-control-plane/envoy/extensions/wasm/v3"

	"slime.io/slime/framework/util"
	"slime.io/slime/modules/plugin/api/v1alpha1"

	"github.com/gogo/protobuf/types"
	istio "istio.io/api/networking/v1alpha3"
)

// translate EnvoyPlugin
func translatePluginToPatch(name, typeurl string, setting *types.Struct) *istio.EnvoyFilter_Patch {
	patch := &istio.EnvoyFilter_Patch{}
	patch.Value = &types.Struct{
		Fields: map[string]*types.Value{
			util.Struct_HttpFilter_TypedPerFilterConfig: {
				Kind: &types.Value_StructValue{
					StructValue: &types.Struct{
						Fields: map[string]*types.Value{
							name: {
								Kind: &types.Value_StructValue{StructValue: &types.Struct{
									Fields: map[string]*types.Value{
										util.Struct_Any_Value: {
											Kind: &types.Value_StructValue{StructValue: setting},
										},
										util.Struct_Any_TypedUrl: {
											Kind: &types.Value_StringValue{StringValue: typeurl},
										},
										util.Struct_Any_AtType: {
											Kind: &types.Value_StringValue{StringValue: util.TypeUrl_UdpaTypedStruct},
										},
									},
								}},
							},
						},
					},
				},
			},
		},
	}
	return patch
}

func translateRatelimitToPatch(settings *types.Struct, route bool) *istio.EnvoyFilter_Patch {
	patch := &istio.EnvoyFilter_Patch{}
	if route {
		patch.Value = &types.Struct{
			Fields: map[string]*types.Value{
				"route": {
					Kind: &types.Value_StructValue{
						StructValue: settings,
					},
				},
			},
		}
	} else {
		patch.Value = settings
	}
	return patch
}

func (r *EnvoyPluginReconciler) translateEnvoyPlugin(in *v1alpha1.EnvoyPlugin, out *istio.EnvoyFilter) {
	if in.WorkloadSelector != nil {
		out.WorkloadSelector = &istio.WorkloadSelector{
			Labels: in.WorkloadSelector.Labels,
		}
	}
	out.ConfigPatches = make([]*istio.EnvoyFilter_EnvoyConfigObjectPatch, 0)

	type target struct {
		applyTo     istio.EnvoyFilter_ApplyTo
		host, route string
	}

	var targets []target
	for _, h := range in.Host {
		targets = append(targets, target{
			applyTo: istio.EnvoyFilter_VIRTUAL_HOST,
			host:    h,
		})
	}
	for _, fullRoute := range in.Route {
		host, route := "", fullRoute
		if ss := strings.SplitN(fullRoute, "/", 2); len(ss) == 2 {
			host, route = ss[0], ss[1]
		}

		targets = append(targets, target{
			applyTo: istio.EnvoyFilter_HTTP_ROUTE,
			host:    host,
			route:   route,
		})
	}

	for _, t := range targets {
		for _, p := range in.Plugins {
			if p.PluginSettings == nil {
				log.Errorf("empty setting, cause error happend, skip plugin build, plugin: %s", p.Name)
				continue
			}

			patchCtx := istio.EnvoyFilter_ANY
			if !strings.HasPrefix(t.host, "inbound|") { // keep backward compatibility
				switch p.ListenerType {
				case v1alpha1.Plugin_Outbound:
					patchCtx = istio.EnvoyFilter_SIDECAR_OUTBOUND
				case v1alpha1.Plugin_Inbound:
					patchCtx = istio.EnvoyFilter_SIDECAR_INBOUND
				}
			}

			var cfp *istio.EnvoyFilter_EnvoyConfigObjectPatch
			switch m := p.PluginSettings.(type) {
			case *v1alpha1.Plugin_Wasm:
				log.Errorf("implentment, cause wasm not been support in envoyplugin settings, skip plugin build, plugin: %s")
				continue
			case *v1alpha1.Plugin_Inline:
				vhost := &istio.EnvoyFilter_RouteConfigurationMatch_VirtualHostMatch{
					Name: t.host,
				}
				if t.applyTo == istio.EnvoyFilter_HTTP_ROUTE {
					vhost.Route = &istio.EnvoyFilter_RouteConfigurationMatch_RouteMatch{
						Name: t.route,
					}
				}
				cfp = &istio.EnvoyFilter_EnvoyConfigObjectPatch{
					ApplyTo: t.applyTo,
					Match: &istio.EnvoyFilter_EnvoyConfigObjectMatch{
						Context: patchCtx,
						ObjectTypes: &istio.EnvoyFilter_EnvoyConfigObjectMatch_RouteConfiguration{
							RouteConfiguration: &istio.EnvoyFilter_RouteConfigurationMatch{
								Vhost: vhost,
							},
						},
					},
				}
				if p.Name == util.Envoy_Ratelimit || p.Name == util.Envoy_Cors {
					cfp.Patch = translateRatelimitToPatch(m.Inline.Settings, t.applyTo == istio.EnvoyFilter_HTTP_ROUTE)
				} else {
					cfp.Patch = translatePluginToPatch(p.Name, p.TypeUrl, m.Inline.Settings)
				}
				cfp.Patch.Operation = istio.EnvoyFilter_Patch_MERGE
			}
			out.ConfigPatches = append(out.ConfigPatches, cfp)
		}
	}
}

// translate PluginManager
func (r *PluginManagerReconciler) translatePluginManager(in *v1alpha1.PluginManager, out *istio.EnvoyFilter) {
	out.WorkloadSelector = &istio.WorkloadSelector{
		Labels: in.WorkloadLabels,
	}
	out.ConfigPatches = make([]*istio.EnvoyFilter_EnvoyConfigObjectPatch, 0)
	for i := range in.Plugin {
		p := in.Plugin[len(in.Plugin)-i-1]
		patch, err := r.convertPluginToPatch(p)
		if err != nil {
			log.Errorf("cause error happened, skip plugin build, plugin: %s, %+v", p.Name, err)
			continue
		}
		out.ConfigPatches = append(out.ConfigPatches, patch)
	}
}

func (r *PluginManagerReconciler) convertPluginToPatch(in *v1alpha1.Plugin) (*istio.EnvoyFilter_EnvoyConfigObjectPatch, error) {
	out := &istio.EnvoyFilter_EnvoyConfigObjectPatch{
		ApplyTo: istio.EnvoyFilter_HTTP_FILTER,
		Match: &istio.EnvoyFilter_EnvoyConfigObjectMatch{
			ObjectTypes: &istio.EnvoyFilter_EnvoyConfigObjectMatch_Listener{
				Listener: &istio.EnvoyFilter_ListenerMatch{
					FilterChain: &istio.EnvoyFilter_ListenerMatch_FilterChainMatch{
						Filter: &istio.EnvoyFilter_ListenerMatch_FilterMatch{
							Name: util.Envoy_HttpConnectionManager,
							SubFilter: &istio.EnvoyFilter_ListenerMatch_SubFilterMatch{
								Name: util.Envoy_Route,
							},
						},
					},
				},
			},
		},
		Patch: &istio.EnvoyFilter_Patch{
			Operation: istio.EnvoyFilter_Patch_INSERT_BEFORE,
			Value: &types.Struct{
				Fields: map[string]*types.Value{},
			},
		},
	}

	if in.ListenerType == v1alpha1.Plugin_Inbound {
		out.Match.Context = istio.EnvoyFilter_SIDECAR_INBOUND
	} else {
		out.Match.Context = istio.EnvoyFilter_SIDECAR_OUTBOUND
	}

	var err error
	if in.PluginSettings != nil {
		switch m := in.PluginSettings.(type) {
		case *v1alpha1.Plugin_Wasm:
			out.Patch.Value.Fields[util.Struct_Wasm_Name] = &types.Value{
				Kind: &types.Value_StringValue{
					StringValue: util.Envoy_FilterHttpWasm,
				},
			}

			if m.Wasm.RootID == "" {
				err = fmt.Errorf("plugin:%s, wasm插件rootID丢失", in.Name)
			} else if m.Wasm.FileName == "" {
				err = fmt.Errorf("plugin: %s, wasm 文件缺失", in.Name)
			} else {
				if err == nil {
					filepath := r.wasm.Get(m.Wasm.FileName)
					pluginConfig := &envoy_extensions_wasm_v3.PluginConfig{
						Name:   in.Name,
						RootId: m.Wasm.RootID,
						Vm: &envoy_extensions_wasm_v3.PluginConfig_VmConfig{
							VmConfig: &envoy_extensions_wasm_v3.VmConfig{
								VmId:    in.Name,
								Runtime: util.Envoy_WasmV8,
								Code: &envoy_config_core_v3.AsyncDataSource{
									Specifier: &envoy_config_core_v3.AsyncDataSource_Local{
										Local: &envoy_config_core_v3.DataSource{
											Specifier: &envoy_config_core_v3.DataSource_Filename{
												Filename: filepath,
											},
										},
									},
								},
							},
						},
					}
					settings, err := util.MessageToStruct(pluginConfig)
					if m.Wasm.Settings != nil {
						isStringSettings := false

						// string类型的配置解析为 google.protobuf.StringValue
						if len(m.Wasm.Settings.Fields) == 1 && m.Wasm.Settings.Fields["_string"] != nil {
							parseTostring := m.Wasm.Settings.Fields["_string"]
							if s, ok := parseTostring.Kind.(*types.Value_StringValue); ok {
								isStringSettings = true
								settings.Fields[util.Struct_Wasm_Configuration] = &types.Value{
									Kind: &types.Value_StructValue{
										StructValue: &types.Struct{
											Fields: map[string]*types.Value{
												util.Struct_Any_AtType: {
													Kind: &types.Value_StringValue{StringValue: util.TypeUrl_StringValue},
												},
												util.Struct_Any_Value: {
													Kind: s,
												},
											},
										},
									},
								}
							}
						}

						// 非string类型的配置解析为 "type.googleapis.com/udpa.type.v1.TypedStruct"
						if !isStringSettings {
							settings.Fields[util.Struct_Wasm_Configuration] = &types.Value{
								Kind: &types.Value_StructValue{
									StructValue: &types.Struct{
										Fields: map[string]*types.Value{
											util.Struct_Any_AtType: {
												Kind: &types.Value_StringValue{StringValue: util.TypeUrl_UdpaTypedStruct},
											},
											util.Struct_Any_Value: {
												Kind: &types.Value_StructValue{StructValue: m.Wasm.Settings},
											},
										},
									},
								},
							}
						}
					}
					if err == nil {
						out.Patch.Value.Fields[util.Struct_HttpFilter_TypedConfig] = &types.Value{
							Kind: &types.Value_StructValue{
								StructValue: &types.Struct{
									Fields: map[string]*types.Value{
										util.Struct_Any_TypedUrl: {
											Kind: &types.Value_StringValue{StringValue: util.TypeUrl_EnvoyFilterHttpWasm},
										},
										util.Struct_Any_AtType: {
											Kind: &types.Value_StringValue{StringValue: util.TypeUrl_UdpaTypedStruct},
										},
										util.Struct_Any_Value: {
											Kind: &types.Value_StructValue{StructValue: &types.Struct{
												Fields: map[string]*types.Value{
													util.Struct_Wasm_Config: {
														Kind: &types.Value_StructValue{
															StructValue: settings,
														},
													},
												},
											}},
										},
									},
								},
							},
						}
					}
				}
			}
		case *v1alpha1.Plugin_Inline:
			out.Patch.Value.Fields[util.Struct_HttpFilter_TypedConfig] = &types.Value{
				Kind: &types.Value_StructValue{
					StructValue: &types.Struct{
						Fields: map[string]*types.Value{
							util.Struct_Any_TypedUrl: {
								Kind: &types.Value_StringValue{StringValue: in.TypeUrl},
							},
							util.Struct_Any_AtType: {
								Kind: &types.Value_StringValue{StringValue: util.TypeUrl_UdpaTypedStruct},
							},
							util.Struct_Any_Value: {
								Kind: &types.Value_StructValue{StructValue: m.Inline.Settings},
							},
						},
					},
				},
			}
			out.Patch.Value.Fields[util.Struct_HttpFilter_Name] = &types.Value{
				Kind: &types.Value_StringValue{
					StringValue: in.Name,
				},
			}
		}
	} else {
		out.Patch.Value.Fields[util.Struct_HttpFilter_Name] = &types.Value{
			Kind: &types.Value_StringValue{
				StringValue: in.Name,
			},
		}
	}
	if err != nil {
		return nil, err
	}
	return out, nil
}
