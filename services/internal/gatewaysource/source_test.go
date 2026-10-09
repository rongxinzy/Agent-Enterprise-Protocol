package gatewaysource

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func window() url.Values {
	return url.Values{"start": {time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)}, "end": {time.Now().UTC().Format(time.RFC3339)}}
}

func TestNativeTransport(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer source-token" || r.Header.Get("X-Scope-OrgID") != "tenant-a" {
			t.Error("source authentication")
		}
		if r.Method == http.MethodPost && r.FormValue("quota") != "123" {
			t.Error("native form")
		}
		_, _ = fmt.Fprint(w, `{"status":"success","data":{"value":"1.234567890123456789"}}`)
	}))
	defer server.Close()
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		data, err := Fetch(context.Background(), server.URL, "source-token", "tenant-a", "/native", method, url.Values{"quota": {"123"}})
		if err != nil || !strings.Contains(string(data), "1.234567890123456789") {
			t.Fatalf("preserve native bytes: %s %v", data, err)
		}
	}
	for _, body := range []string{`not json`, `{"status":"error","error":"secret"}`, strings.Repeat(" ", 4<<20) + `{}`} {
		bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = fmt.Fprint(w, body) }))
		if _, err := Fetch(context.Background(), bad.URL, "", "tenant", "", http.MethodGet, nil); err == nil {
			t.Fatal("bad payload accepted")
		}
		bad.Close()
	}
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, server.URL, http.StatusFound) }))
	defer redirect.Close()
	if _, err := Fetch(context.Background(), redirect.URL, "", "tenant", "", http.MethodGet, nil); err == nil {
		t.Fatal("redirect followed")
	}
	for _, endpoint := range []string{"", "file:///a", "http://user:pass@example.test", "http://host?q=x", "http://host#x", "http://127.0.0.1:1"} {
		if _, err := Fetch(context.Background(), endpoint, "", "tenant", "", http.MethodGet, nil); err == nil {
			t.Fatal("invalid source accepted")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Fetch(ctx, server.URL, "", "tenant", "", http.MethodGet, nil); err == nil {
		t.Fatal("cancel ignored")
	}
}

func TestNativeMetricQueries(t *testing.T) {
	for _, metric := range Metrics {
		for _, group := range []string{"none", "model", "route", "user"} {
			values := window()
			values.Set("metric", metric)
			values.Set("groupBy", group)
			q, err := MetricQuery(`tenant\"}{`, values)
			if err != nil || !strings.Contains(q.Get("query"), base64.RawURLEncoding.EncodeToString([]byte(`tenant\"}{`))) {
				t.Fatalf("tenant bound query: %s %v", q, err)
			}
		}
	}
	v := window()
	v.Set("metric", "input_tokens")
	v.Set("userId", `user"}`)
	v.Set("modelId", `model"}`)
	q, err := MetricQuery("tenant", v)
	if err != nil || !strings.Contains(q.Get("query"), Consumer("tenant", `user"}`)) || strings.Contains(q.Get("query"), `ai_model="model"}`) {
		t.Fatal("escaping failed")
	}
	for _, key := range []string{"teamId", "roleId", "groupBy", "metric"} {
		v := window()
		v.Set("metric", "calls")
		v.Set(key, "unsupported")
		if _, err := MetricQuery("tenant", v); err != ErrDimension {
			t.Fatal(key, err)
		}
	}
	for _, metric := range InfrastructureMetrics {
		v := window()
		v.Set("metric", metric)
		if _, err := InfrastructureQuery(v); err != nil {
			t.Fatal(metric, err)
		}
	}
	v = window()
	v.Set("metric", "upstream_qps")
	v.Set("userId", "user")
	if _, err := InfrastructureQuery(v); err != ErrDimension {
		t.Fatal("unscoped dimension allowed")
	}
}

func TestNativeLogQueries(t *testing.T) {
	v := window()
	v.Set("userId", "user-a")
	v.Set("modelId", "model-a")
	v.Set("teamId", "team-a")
	v.Set("roleId", "role-a")
	q, err := LogQuery(`tenant"}`, `request"}`, v)
	if err != nil || !strings.Contains(q.Get("query"), "line_format") || !strings.Contains(q.Get("query"), `aep_deployment_id="tenant\"}"`) {
		t.Fatalf("query: %s %v", q, err)
	}
	end, _ := time.Parse(time.RFC3339, v.Get("end"))
	v.Set("cursor", fmt.Sprint(end.UnixNano()))
	q, err = LogQuery("tenant", "", v)
	if err != nil || q.Get("end") != fmt.Sprint(end.UnixNano()-1) {
		t.Fatal("exclusive cursor")
	}
	for _, source := range []string{"all", "gateway", "authorizer"} {
		v := window()
		v.Set("source", source)
		if _, err := LogQuery("tenant", "", v); err != nil {
			t.Fatal(err)
		}
	}
	for _, item := range [][2]string{{"limit", "201"}, {"limit", "bad"}, {"source", "bad"}, {"cursor", "bad"}, {"cursor", "1"}} {
		v := window()
		v.Set(item[0], item[1])
		if _, err := LogQuery("tenant", "", v); err != ErrQuery {
			t.Fatal(item, err)
		}
	}
}

func TestWindowBounds(t *testing.T) {
	for _, item := range [][2]string{{"start", "bad"}, {"end", "bad"}, {"step", "0"}, {"step", "3601"}, {"step", "bad"}, {"end", time.Now().Add(time.Hour).UTC().Format(time.RFC3339)}, {"start", time.Now().Add(-40 * 24 * time.Hour).UTC().Format(time.RFC3339)}} {
		v := window()
		v.Set(item[0], item[1])
		if _, _, _, err := Window(v); err == nil {
			t.Fatal(item)
		}
	}
	v := window()
	v.Set("start", v.Get("end"))
	if _, _, _, err := Window(v); err == nil {
		t.Fatal("empty window")
	}
	v = window()
	v.Set("start", time.Now().Add(-24*time.Hour).UTC().Format(time.RFC3339))
	v.Set("step", "1")
	if _, _, _, err := Window(v); err == nil {
		t.Fatal("too many points")
	}
}

func TestOrganizationNativeQueries(t *testing.T) {
	for _, metric := range Metrics {
		for _, group := range []string{"none", "model", "user", "route", "team", "role"} {
			v := window()
			v.Set("metric", metric)
			v.Set("groupBy", group)
			v.Set("teamId", "team-a")
			v.Set("roleId", "role-a")
			v.Set("userId", "user-a")
			v.Set("modelId", "model-a")
			q, err := LogMetricQuery("tenant-a", v)
			if err != nil || !strings.Contains(q.Get("query"), `aep_deployment_id="tenant-a"`) || !strings.Contains(q.Get("query"), "aep_dimension") {
				t.Fatal(q, err)
			}
		}
	}
	for _, key := range []string{"metric", "groupBy", "start"} {
		v := window()
		v.Set("metric", "calls")
		v.Set(key, "bad")
		if _, err := LogMetricQuery("tenant", v); err == nil {
			t.Fatal(key)
		}
	}
}
