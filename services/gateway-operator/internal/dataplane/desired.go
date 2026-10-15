// Package dataplane verifies loaded Envoy configuration, independently of SSA.
// Dumps and rendered resources can contain credentials: keep both in memory and
// return only fixed diagnostics. This is configuration readiness, not provider health.
package dataplane

import (
	"encoding/json"
	"errors"
	"strings"

	cluster "github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3"
	route "github.com/envoyproxy/go-control-plane/envoy/config/route/v3"
	_ "github.com/envoyproxy/go-control-plane/envoy/extensions/transport_sockets/tls/v3"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"sigs.k8s.io/yaml"

	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/internal/gatewayrender"
)

var errUnsupported = errors.New("unsupported data-plane configuration")
var errMismatch = errors.New("loaded data-plane configuration differs from desired snapshot")

type ingress struct {
	Name, Path, Model string
	Cluster           *cluster.Cluster
	Patch             *route.Route
}

type plugin struct {
	Name, Digest string
	Rules        []any
	FailOpen     bool
}

// Desired contains private, credential-bearing verification inputs.
type Desired struct {
	ingresses map[string]*ingress
	plugins   map[string]*plugin
	forbidden map[string]bool
}

// Compile consumes the existing renderer, without a second business-to-Higress
// translator. Forbidden names cover disabled/deleted route and policy tombstones.
func Compile(resources []gatewayrender.RenderedResource, forbidden []string) (*Desired, error) {
	d := &Desired{ingresses: map[string]*ingress{}, plugins: map[string]*plugin{}, forbidden: map[string]bool{}}
	for _, name := range forbidden {
		d.forbidden[name] = true
	}
	for _, resource := range resources {
		var obj struct {
			Kind     string `json:"kind"`
			Metadata struct {
				Name, Namespace string
				Annotations     map[string]string
			} `json:"metadata"`
			Spec struct {
				Rules []struct {
					HTTP struct {
						Paths []struct {
							Path, PathType string
							Backend        struct {
								Service struct {
									Name string
									Port struct{ Number int }
								}
							}
						}
					}
				} `json:"rules"`
				URL, FailStrategy    string
				DefaultConfigDisable bool
				MatchRules           []struct {
					Config        map[string]any
					Ingress       []string
					ConfigDisable bool
				}
				ConfigPatches []struct {
					ApplyTo string
					Match   struct {
						RouteConfiguration struct {
							Vhost struct{ Route struct{ Name string } }
						}
					}
					Patch struct {
						Operation string
						Value     json.RawMessage
					}
				}
			} `json:"spec"`
		}
		if yaml.Unmarshal([]byte(resource.Body), &obj) != nil {
			return nil, errUnsupported
		}
		if obj.Metadata.Namespace != "higress-system" || obj.Metadata.Name == "" {
			return nil, errUnsupported
		}
		name := obj.Metadata.Name
		switch obj.Kind {
		case "Ingress":
			if len(obj.Spec.Rules) != 1 || len(obj.Spec.Rules[0].HTTP.Paths) != 1 {
				return nil, errUnsupported
			}
			p := obj.Spec.Rules[0].HTTP.Paths[0]
			if p.PathType != "Prefix" || p.Path == "" || p.Backend.Service.Name != "aep-model-gateway" || p.Backend.Service.Port.Number != 80 {
				return nil, errUnsupported
			}
			d.ingresses[name] = &ingress{Name: name, Path: strings.TrimRight(p.Path, "/"), Model: obj.Metadata.Annotations["higress.io/exact-match-header-x-aep-model-id"]}
		case "WasmPlugin":
			parts := strings.Split(obj.Spec.URL, "@sha256:")
			if len(parts) != 2 || len(parts[1]) != 64 || !obj.Spec.DefaultConfigDisable {
				return nil, errUnsupported
			}
			p := &plugin{Name: "extensions.istio.io/wasmplugin/higress-system." + name, Digest: parts[1], FailOpen: obj.Spec.FailStrategy == "FAIL_OPEN"}
			for _, r := range obj.Spec.MatchRules {
				if r.ConfigDisable || len(r.Ingress) == 0 {
					return nil, errUnsupported
				}
				rule := map[string]any{}
				for k, v := range r.Config {
					rule[k] = v
				}
				rule["_match_route_"] = r.Ingress
				// Round-trip normalizes []string and JSON numbers once, never logs values.
				raw, _ := json.Marshal(rule)
				var normalized any
				if json.Unmarshal(raw, &normalized) != nil {
					return nil, errUnsupported
				}
				p.Rules = append(p.Rules, normalized)
			}
			d.plugins[p.Name] = p
		case "EnvoyFilter": // Linked after all ingress documents have been collected.
		default:
			return nil, errUnsupported
		}
	}
	for _, resource := range resources {
		var obj struct {
			Kind     string
			Metadata struct{ Name string }
			Spec     struct {
				ConfigPatches []struct {
					ApplyTo string
					Match   struct {
						RouteConfiguration struct {
							Vhost struct{ Route struct{ Name string } }
						}
					}
					Patch struct {
						Operation string
						Value     json.RawMessage
					}
				}
			}
		}
		if yaml.Unmarshal([]byte(resource.Body), &obj) != nil {
			return nil, errUnsupported
		}
		if obj.Kind != "EnvoyFilter" {
			continue
		}
		i := d.ingresses[obj.Metadata.Name]
		if i == nil || len(obj.Spec.ConfigPatches) != 2 {
			return nil, errUnsupported
		}
		for _, p := range obj.Spec.ConfigPatches {
			switch {
			case p.ApplyTo == "CLUSTER" && p.Patch.Operation == "ADD":
				i.Cluster = &cluster.Cluster{}
				if protojson.Unmarshal(p.Patch.Value, i.Cluster) != nil || i.Cluster.Name != i.Name {
					return nil, errUnsupported
				}
			case p.ApplyTo == "HTTP_ROUTE" && p.Patch.Operation == "MERGE" && p.Match.RouteConfiguration.Vhost.Route.Name == i.Name:
				i.Patch = &route.Route{}
				if protojson.Unmarshal(p.Patch.Value, i.Patch) != nil {
					return nil, errUnsupported
				}
			default:
				return nil, errUnsupported
			}
		}
		if i.Cluster == nil || i.Patch == nil {
			return nil, errUnsupported
		}
	}
	for name := range d.ingresses {
		if d.forbidden[name] {
			return nil, errUnsupported
		}
	}
	return d, nil
}

// canonical uses official Envoy protobuf normalization (field names, enums,
// durations, wrapper values), then compares only renderer-owned fields.
func canonical(m proto.Message) (any, error) {
	raw, err := (protojson.MarshalOptions{UseProtoNames: true}).Marshal(m)
	if err != nil {
		return nil, errUnsupported
	}
	var out any
	if json.Unmarshal(raw, &out) != nil {
		return nil, errUnsupported
	}
	return out, nil
}

func contains(actual, expected any) bool {
	switch e := expected.(type) {
	case map[string]any:
		a, ok := actual.(map[string]any)
		if !ok {
			return false
		}
		for k, v := range e {
			if !contains(a[k], v) {
				return false
			}
		}
		return true
	case []any:
		a, ok := actual.([]any)
		if !ok || len(a) != len(e) {
			return false
		}
		for i := range e {
			if !contains(a[i], e[i]) {
				return false
			}
		}
		return true
	default:
		return actual == expected
	}
}
