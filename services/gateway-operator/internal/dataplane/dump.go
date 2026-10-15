package dataplane

import (
	"encoding/json"
	"path"
	"reflect"
	"strings"

	cluster "github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3"
	route "github.com/envoyproxy/go-control-plane/envoy/config/route/v3"
	wasm "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/http/wasm/v3"
	wasmcfg "github.com/envoyproxy/go-control-plane/envoy/extensions/wasm/v3"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

const MaxDumpBytes = 16 << 20

type rawObject map[string]json.RawMessage

func decode(raw json.RawMessage, out any) bool {
	return len(raw) > 0 && json.Unmarshal(raw, out) == nil
}
func object(raw json.RawMessage) rawObject {
	var out rawObject
	_ = json.Unmarshal(raw, &out)
	return out
}
func objects(raw json.RawMessage) []rawObject {
	var out []rawObject
	_ = json.Unmarshal(raw, &out)
	return out
}
func stringField(o rawObject, k string) string { var s string; _ = json.Unmarshal(o[k], &s); return s }
func withoutType(raw json.RawMessage) []byte {
	o := object(raw)
	delete(o, "@type")
	b, _ := json.Marshal(o)
	return b
}

type manager struct {
	routes  []string
	filters map[string]bool
}
type loadedRoute struct {
	config string
	value  rawObject
}
type loaded struct {
	routes   map[string][]loadedRoute
	clusters map[string]json.RawMessage
	plugins  map[string]json.RawMessage
	managers []manager
}

// Verify checks active configuration only. Warming listeners/clusters and
// unattached RDS/ECDS caches never constitute effectiveness evidence.
func (d *Desired) Verify(raw []byte) error {
	if len(raw) == 0 || len(raw) > MaxDumpBytes {
		return errUnsupported
	}
	l, err := parseDump(raw)
	if err != nil {
		return err
	}
	for name := range d.forbidden {
		if len(l.routes[name]) > 0 || l.clusters[name] != nil || l.plugins["extensions.istio.io/wasmplugin/higress-system."+name] != nil {
			return errMismatch
		}
	}
	for name, i := range d.ingresses {
		if err := d.verifyIngress(l, name, i); err != nil {
			return err
		}
	}
	for name, p := range d.plugins {
		if err := verifyPlugin(l, name, p); err != nil {
			return err
		}
	}
	return nil
}

func parseDump(raw []byte) (*loaded, error) {
	var envelope struct{ Configs []json.RawMessage }
	if json.Unmarshal(raw, &envelope) != nil || len(envelope.Configs) == 0 {
		return nil, errUnsupported
	}
	l := &loaded{routes: map[string][]loadedRoute{}, clusters: map[string]json.RawMessage{}, plugins: map[string]json.RawMessage{}}
	scoped := map[string][]string{}
	var listeners []rawObject
	for _, raw := range envelope.Configs {
		c := object(raw)
		switch stringField(c, "@type") {
		case "type.googleapis.com/envoy.admin.v3.RoutesConfigDump":
			for _, entry := range objects(c["dynamic_route_configs"]) {
				rc := object(entry["route_config"])
				name := stringField(rc, "name")
				for _, v := range objects(rc["virtual_hosts"]) {
					var domains []string
					if !decode(v["domains"], &domains) || len(domains) != 1 || domains[0] != "*" {
						continue
					}
					for _, r := range objects(v["routes"]) {
						n := stringField(r, "name")
						l.routes[n] = append(l.routes[n], loadedRoute{name, r})
					}
				}
			}
		case "type.googleapis.com/envoy.admin.v3.ClustersConfigDump":
			for _, entry := range objects(c["dynamic_active_clusters"]) {
				c := entry["cluster"]
				n := stringField(object(c), "name")
				if l.clusters[n] != nil {
					return nil, errUnsupported
				}
				l.clusters[n] = c
			}
		case "type.googleapis.com/envoy.admin.v3.EcdsConfigDump":
			for _, entry := range objects(c["ecds_filters"]) {
				f := object(entry["ecds_filter"])
				n := stringField(f, "name")
				if l.plugins[n] != nil {
					return nil, errUnsupported
				}
				l.plugins[n] = f["typed_config"]
			}
		case "type.googleapis.com/envoy.admin.v3.ScopedRoutesConfigDump":
			for _, entry := range objects(c["dynamic_scoped_route_configs"]) {
				for _, rc := range objects(entry["scoped_route_configs"]) {
					fragments := objects(object(rc["key"])["fragments"])
					if len(fragments) == 2 && stringField(fragments[0], "string_key") == "80" && stringField(fragments[1], "string_key") == "*" {
						scoped[stringField(entry, "name")] = append(scoped[stringField(entry, "name")], stringField(rc, "route_configuration_name"))
					}
				}
			}
		case "type.googleapis.com/envoy.admin.v3.ListenersConfigDump":
			for _, entry := range objects(c["dynamic_listeners"]) {
				active := object(entry["active_state"])
				if active["listener"] != nil {
					listeners = append(listeners, object(active["listener"]))
				}
			}
		}
	}
	for _, listener := range listeners {
		var port int
		socket := object(object(listener["address"])["socket_address"])
		if !decode(socket["port_value"], &port) || port != 80 {
			continue
		}
		for _, chain := range objects(listener["filter_chains"]) {
			for _, nf := range objects(chain["filters"]) {
				h := object(nf["typed_config"])
				if stringField(h, "@type") != "type.googleapis.com/envoy.extensions.filters.network.http_connection_manager.v3.HttpConnectionManager" {
					continue
				}
				m := manager{filters: map[string]bool{}}
				if s := object(h["scoped_routes"]); s != nil {
					var builder any
					if !decode(s["scope_key_builder"], &builder) {
						continue
					}
					var supported any
					_ = json.Unmarshal([]byte(`{"fragments":[{"local_port_value_extractor":{}},{"host_value_extractor":{}}]}`), &supported)
					if !reflect.DeepEqual(builder, supported) || object(s["scoped_rds"])["scoped_rds_config_source"] == nil {
						continue
					}
					m.routes = scoped[stringField(s, "name")]
				} else if r := object(h["rds"]); r != nil {
					m.routes = []string{stringField(r, "route_config_name")}
				}
				for _, f := range objects(h["http_filters"]) {
					var disabled bool
					_ = json.Unmarshal(f["disabled"], &disabled)
					m.filters[stringField(f, "name")] = !disabled && f["config_discovery"] != nil
				}
				if len(m.routes) > 0 {
					l.managers = append(l.managers, m)
				}
			}
		}
	}
	if len(l.managers) == 0 {
		return nil, errUnsupported
	}
	return l, nil
}

func (d *Desired) verifyIngress(l *loaded, name string, i *ingress) error {
	routes := l.routes[name]
	if len(routes) != 2 {
		return errMismatch
	}
	paths := map[string]bool{}
	for _, loaded := range routes {
		r := loaded.value
		match := object(r["match"])
		var sensitive bool
		if !decode(match["case_sensitive"], &sensitive) || !sensitive {
			return errMismatch
		}
		p := stringField(match, "path")
		prefix := stringField(match, "prefix")
		if p == i.Path && prefix == "" {
			paths["exact"] = true
		} else if prefix == i.Path+"/" && p == "" {
			paths["prefix"] = true
		} else {
			return errMismatch
		}
		headers := objects(match["headers"])
		for key := range match {
			if key != "path" && key != "prefix" && key != "case_sensitive" && key != "headers" {
				return errUnsupported
			}
		}
		if i.Model == "" {
			if len(headers) != 0 {
				return errMismatch
			}
		} else {
			if len(headers) != 1 || stringField(headers[0], "name") != "x-aep-model-id" {
				return errMismatch
			}
			h := headers[0]
			var inverted bool
			_ = json.Unmarshal(h["invert_match"], &inverted)
			exact := stringField(h, "exact_match")
			if exact == "" {
				exact = stringField(object(h["string_match"]), "exact")
			}
			if inverted || exact != i.Model {
				return errMismatch
			}
			for key := range h {
				if key != "name" && key != "exact_match" && key != "string_match" && key != "invert_match" {
					return errUnsupported
				}
			}
		}
		if i.Patch != nil {
			// Parse renderer-owned fields with official Envoy types, excluding
			// unrelated extension Any messages from the rest of the route.
			a := &route.Route{}
			selected, _ := json.Marshal(rawObject{"route": r["route"], "request_headers_to_add": defaultArray(r["request_headers_to_add"])})
			parsed := object(selected)
			action := object(parsed["route"])
			delete(action, "retry_policy") // Higress-owned defaults can contain third-party extensions.
			b, _ := json.Marshal(action)
			parsed["route"] = b
			selected, _ = json.Marshal(parsed)
			if protojson.Unmarshal(selected, a) != nil {
				return errUnsupported
			}
			actual, e1 := canonical(a)
			expected, e2 := canonical(i.Patch)
			if e1 != nil || e2 != nil || !contains(actual, expected) {
				return errMismatch
			}
			if a.GetRoute().GetCluster() != i.Patch.GetRoute().GetCluster() || a.GetRoute().GetHostRewriteLiteral() != i.Patch.GetRoute().GetHostRewriteLiteral() {
				return errMismatch
			}
			if !proto.Equal(a.GetRoute().GetRegexRewrite(), i.Patch.GetRoute().GetRegexRewrite()) || a.GetRoute().GetPrefixRewrite() != "" || a.GetRoute().GetPathRewritePolicy() != nil || len(a.GetRoute().GetRequestMirrorPolicies()) != 0 || len(r["request_headers_to_remove"]) > 0 {
				return errMismatch
			}
		} else if stringField(object(r["route"]), "cluster") != "outbound|80||aep-model-gateway.higress-system.svc.cluster.local" {
			return errMismatch
		}
		attached := false
		for _, m := range l.managers {
			if !has(m.routes, loaded.config) {
				continue
			}
			attached = true
			for _, p := range d.plugins {
				if len(p.Rules) > 0 && !m.filters[p.Name] {
					return errMismatch
				}
			}
		}
		if !attached {
			return errMismatch
		}
	}
	if len(paths) != 2 {
		return errMismatch
	}
	if i.Cluster != nil {
		c := &cluster.Cluster{}
		if protojson.Unmarshal(withoutType(l.clusters[name]), c) != nil {
			return errUnsupported
		}
		actual, e1 := canonical(c)
		expected, e2 := canonical(i.Cluster)
		if e1 != nil || e2 != nil || !contains(actual, expected) || (c.TransportSocket == nil) != (i.Cluster.TransportSocket == nil) {
			return errMismatch
		}
		if len(c.GetTransportSocketMatches()) != 0 || c.GetClusterType() != nil {
			return errUnsupported
		}
	}
	return nil
}

func defaultArray(raw json.RawMessage) json.RawMessage {
	if raw == nil {
		return json.RawMessage("[]")
	}
	return raw
}
func has(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func verifyPlugin(l *loaded, name string, p *plugin) error {
	raw := l.plugins[name]
	if raw == nil {
		if len(p.Rules) == 0 {
			return nil
		}
		return errMismatch
	}
	if stringField(object(raw), "@type") != "type.googleapis.com/envoy.extensions.filters.http.wasm.v3.Wasm" {
		return errMismatch
	}
	w := &wasm.Wasm{}
	if protojson.Unmarshal(withoutType(raw), w) != nil {
		return errUnsupported
	}
	cfg := w.GetConfig()
	if cfg == nil {
		return errMismatch
	}
	// Higress 2.2.4 emits the legacy fail_open wire field. Read that wire value
	// explicitly while honoring the current Envoy failure_policy when present.
	var failOpen bool
	_ = json.Unmarshal(object(object(raw)["config"])["fail_open"], &failOpen)
	switch cfg.GetFailurePolicy() {
	case wasmcfg.FailurePolicy_UNSPECIFIED:
	case wasmcfg.FailurePolicy_FAIL_OPEN:
		failOpen = true
	case wasmcfg.FailurePolicy_FAIL_CLOSED:
		failOpen = false
	default:
		return errUnsupported
	}
	if failOpen != p.FailOpen {
		return errMismatch
	}
	file := cfg.GetVmConfig().GetCode().GetLocal().GetFilename()
	if path.Base(file) != p.Digest+".wasm" {
		return errMismatch
	}
	s := &wrapperspb.StringValue{}
	if cfg.GetConfiguration().UnmarshalTo(s) != nil {
		return errUnsupported
	}
	var content struct {
		Rules []any `json:"_rules_"`
	}
	if json.Unmarshal([]byte(s.Value), &content) != nil {
		return errUnsupported
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal([]byte(s.Value), &fields) != nil || len(fields) != 1 || fields["_rules_"] == nil {
		return errUnsupported
	}
	if len(content.Rules) == 0 && len(p.Rules) == 0 {
		return nil
	}
	// Exact comparison also rejects extra matching routes or stale credentials.
	if !reflect.DeepEqual(content.Rules, p.Rules) {
		return errMismatch
	}
	if strings.Contains(file, "..") {
		return errUnsupported
	}
	return nil
}
