package gatewaysource

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var ErrQuery = errors.New("invalid gateway query")
var ErrDimension = errors.New("native source does not support the requested dimension")

var Metrics = []string{"input_tokens", "output_tokens", "calls", "failures", "first_token_duration", "service_duration"}

var InfrastructureMetrics = []string{"downstream_qps", "upstream_qps", "downstream_success_rate", "upstream_success_rate", "auth_requests"}

// InfrastructureQuery is only allowed for an operator-bound dedicated data
// source: native Envoy connection-manager counters have no consumer dimension.
func InfrastructureQuery(values url.Values) (url.Values, error) {
	start, end, step, err := Window(values)
	if err != nil {
		return nil, err
	}
	if (values.Get("groupBy") != "" && values.Get("groupBy") != "none") || values.Get("modelId") != "" || values.Get("userId") != "" || values.Get("teamId") != "" || values.Get("roleId") != "" {
		return nil, ErrDimension
	}
	var query string
	// Higress standalone uses "http"; Kubernetes listeners use
	// "outbound_<address>_<port>". Never include admin/agent/stats listeners.
	downstream := `{http_conn_manager_prefix=~"http|outbound_.*"}`
	upstream := `{cluster_name!~"agent|prometheus_stats|xds-grpc(\\.internal)?|sds-grpc(\\.internal)?"}`
	rate := func(metric, selector string) string { return "sum(irate(" + metric + selector + "[2m]))" }
	// The response-class metric is native Envoy output. If no 5xx series
	// exists, use a zero derived from the present total in Prometheus; omit
	// idle/absent totals instead of returning NaN or inventing a success rate.
	success := func(total, failures string) string {
		return "(1 - (" + failures + " or (0 * " + total + ")) / " + total + ") and (" + total + " > 0)"
	}
	switch values.Get("metric") {
	case "downstream_qps":
		query = rate("envoy_http_downstream_rq_total", downstream)
	case "upstream_qps":
		query = rate("envoy_cluster_upstream_rq_total", upstream)
	case "downstream_success_rate":
		query = success(rate("envoy_http_downstream_rq_total", downstream), rate("envoy_http_downstream_rq", strings.TrimSuffix(downstream, "}")+`,response_code_class="5xx"}`))
	case "upstream_success_rate":
		query = success(rate("envoy_cluster_upstream_rq_total", upstream), rate("envoy_cluster_upstream_rq", strings.TrimSuffix(upstream, "}")+`,response_code_class="5xx"}`))
	case "auth_requests":
		query = `round(sum by (status)(increase(aep_gateway_authorizer_http_requests_total{route!~"/healthz|/readyz|/livez|/metrics"}[` + strconv.Itoa(step) + `s])))`
	default:
		return nil, ErrDimension
	}
	return url.Values{"query": {query}, "start": {start.Format(time.RFC3339Nano)}, "end": {end.Format(time.RFC3339Nano)}, "step": {strconv.Itoa(step)}}, nil
}

// Window enforces a bounded history and output size before contacting a source.
func Window(values url.Values) (time.Time, time.Time, int, error) {
	start, e1 := time.Parse(time.RFC3339Nano, values.Get("start"))
	end, e2 := time.Parse(time.RFC3339Nano, values.Get("end"))
	step := 60
	var e3 error
	if values.Get("step") != "" {
		step, e3 = strconv.Atoi(values.Get("step"))
	}
	if e1 != nil || e2 != nil || e3 != nil || !end.After(start) || end.Sub(start) > 31*24*time.Hour || end.After(time.Now().Add(time.Minute)) || step < 1 || step > 3600 || end.Sub(start)/time.Duration(step)/time.Second > 11000 {
		return time.Time{}, time.Time{}, 0, ErrQuery
	}
	return start, end, step, nil
}

func MetricQuery(tenant string, values url.Values) (url.Values, error) {
	start, end, step, err := Window(values)
	if err != nil {
		return nil, err
	}
	if values.Get("teamId") != "" || values.Get("roleId") != "" {
		return nil, ErrDimension
	}
	group := values.Get("groupBy")
	labels := map[string]string{"": "", "none": "", "model": "ai_model", "route": "ai_route", "user": "ai_consumer"}
	label, ok := labels[group]
	if !ok {
		return nil, ErrDimension
	}
	consumer := `ai_consumer=~` + strconv.Quote(regexp.QuoteMeta(ConsumerPrefix(tenant))+`[A-Za-z0-9_-]+`)
	if user := values.Get("userId"); user != "" {
		consumer = `ai_consumer=` + strconv.Quote(Consumer(tenant, user))
	}
	selector := "{" + consumer
	if model := values.Get("modelId"); model != "" {
		selector += ",ai_model=" + strconv.Quote(model)
	}
	selector += "}"
	prefix := "route_upstream_model_consumer_metric_"
	// These are the official ai-statistics counters and mean-duration formulas.
	// Prometheus evaluates them; AEP returns its native response unchanged.
	aggregate := func(expression string) string {
		if label == "" {
			return "sum(" + expression + ")"
		}
		return "sum by (" + label + ")(" + expression + ")"
	}
	metric := values.Get("metric")
	var query string
	for key, counter := range map[string]string{"input_tokens": "input_token", "output_tokens": "output_token", "calls": "llm_duration_count", "failures": "llm_failure_count"} {
		if metric == key {
			// Round the native aggregate, not each Pod's extrapolated increase.
			// This is an integer presentation of an estimate, not an exact ledger.
			query = "round(" + aggregate("increase("+prefix+counter+selector+"["+strconv.Itoa(step)+"s])") + ")"
		}
	}
	if metric == "first_token_duration" || metric == "service_duration" {
		duration, count := "llm_service_duration", "llm_duration_count"
		if metric == "first_token_duration" {
			duration, count = "llm_first_token_duration", "llm_stream_duration_count"
		}
		query = aggregate("irate("+prefix+duration+selector+"[2m])") + " / " + aggregate("irate("+prefix+count+selector+"[2m])")
	}
	if query == "" {
		return nil, ErrDimension
	}
	return url.Values{"query": {query}, "start": {start.Format(time.RFC3339Nano)}, "end": {end.Format(time.RFC3339Nano)}, "step": {strconv.Itoa(step)}}, nil
}

func LogQuery(tenant, requestID string, values url.Values) (url.Values, error) {
	start, end, _, err := Window(values)
	if err != nil {
		return nil, err
	}
	limit := 100
	if values.Get("limit") != "" {
		limit, err = strconv.Atoi(values.Get("limit"))
	}
	if err != nil || limit < 1 || limit > 200 {
		return nil, ErrQuery
	}
	if cursor := values.Get("cursor"); cursor != "" {
		stamp, err := strconv.ParseInt(cursor, 10, 64)
		if err != nil || stamp <= start.UnixNano() || stamp > end.UnixNano() {
			return nil, ErrQuery
		}
		end = time.Unix(0, stamp-1)
	}
	source := values.Get("source")
	if source == "" || source == "all" {
		source = "gateway|authorizer"
	} else if source != "gateway" && source != "authorizer" {
		return nil, ErrQuery
	}
	query := "{aep_deployment_id=" + strconv.Quote(tenant) + ",aep_dimension=\"base\",aep_source=~" + strconv.Quote(source) + "} | json"
	for key, field := range map[string]string{"modelId": "model_id", "userId": "user_id"} {
		if value := values.Get(key); value != "" {
			query += " | " + field + "=" + strconv.Quote(value)
		}
	}
	if requestID != "" {
		query += " | request_id=" + strconv.Quote(requestID)
	}
	// Membership headers use |<base64url-id>| entries, never primary-only IDs.
	for key, field := range map[string]string{"teamId": "team_ids", "roleId": "role_ids"} {
		if value := values.Get(key); value != "" {
			query += " | " + field + "=~" + strconv.Quote(".*"+regexp.QuoteMeta("|"+strings.TrimPrefix(Consumer("", value), ConsumerPrefix(""))+"|")+".*")
		}
	}
	query += ` | __error__=""`
	query += ` | line_format "{{printf \"{\\\"request_id\\\":%q,\\\"model_id\\\":%q,\\\"user_id\\\":%q,\\\"team_ids\\\":%q,\\\"role_ids\\\":%q,\\\"status\\\":%q,\\\"response_flags\\\":%q,\\\"input_token\\\":%q,\\\"output_token\\\":%q,\\\"llm_service_duration\\\":%q,\\\"llm_first_token_duration\\\":%q}\" .request_id .model_id .user_id .team_ids .role_ids .status .response_flags .input_token .output_token .llm_service_duration .llm_first_token_duration}}"`
	// Only an allowlisted access-log dataset may carry these stream labels.
	// No free-form PromQL/LogQL or upstream URL is accepted from a client.
	return url.Values{"query": {query}, "start": {fmt.Sprint(start.UnixNano())}, "end": {fmt.Sprint(end.UnixNano())}, "direction": {"backward"}, "limit": {strconv.Itoa(limit)}}, nil
}
