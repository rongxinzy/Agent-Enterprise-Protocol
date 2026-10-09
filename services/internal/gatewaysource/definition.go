package gatewaysource

import "net/url"

// MetricDefinition describes a selected native query; it contains no telemetry.
type MetricDefinition struct {
	ID             string `json:"id"`
	Unit           string `json:"unit"`
	Aggregation    string `json:"aggregation"`
	WindowSeconds  int    `json:"windowSeconds"`
	GroupBy        string `json:"groupBy"`
	ModelDimension string `json:"modelDimension"`
}

// DescribeMetric uses the same source selected for the validated query.
// IDs distinguish observations that must not be presented as interchangeable.
func DescribeMetric(source string, values url.Values) (MetricDefinition, error) {
	_, _, step, err := Window(values)
	if err != nil {
		return MetricDefinition{}, err
	}
	definition := MetricDefinition{WindowSeconds: step, GroupBy: values.Get("groupBy"), ModelDimension: "not_applicable"}
	if definition.GroupBy == "" {
		definition.GroupBy = "none"
	}
	if definition.GroupBy == "model" || values.Get("modelId") != "" {
		definition.ModelDimension = "upstream_model"
		if source == "loki" {
			definition.ModelDimension = "catalog_model"
		}
	}
	metric := values.Get("metric")
	if source == "loki" {
		switch metric {
		case "input_tokens", "output_tokens":
			definition.ID, definition.Unit, definition.Aggregation = "gateway_log_"+metric, "tokens", "log_sum"
		case "calls":
			definition.ID, definition.Unit, definition.Aggregation = "gateway_access_requests", "requests", "log_count"
		case "failures":
			definition.ID, definition.Unit, definition.Aggregation = "gateway_http_errors", "requests", "log_count"
		case "first_token_duration", "service_duration":
			definition.ID, definition.Unit, definition.Aggregation = "gateway_log_mean_"+metric, "milliseconds", "log_mean"
		default:
			return MetricDefinition{}, ErrDimension
		}
		return definition, nil
	}
	if source != "prometheus" {
		return MetricDefinition{}, ErrDimension
	}
	switch metric {
	case "input_tokens", "output_tokens":
		definition.ID, definition.Unit, definition.Aggregation = "ai_"+metric, "tokens", "counter_increase"
	case "calls":
		definition.ID, definition.Unit, definition.Aggregation = "ai_usage_completed_calls", "requests", "counter_increase"
	case "failures":
		definition.ID, definition.Unit, definition.Aggregation = "ai_detected_failures", "requests", "counter_increase"
	case "first_token_duration", "service_duration":
		definition.ID, definition.Unit, definition.Aggregation = "ai_usage_mean_"+metric, "milliseconds", "counter_rate_mean"
		definition.WindowSeconds = 120
	case "downstream_qps", "upstream_qps":
		definition.ID, definition.Unit, definition.Aggregation = "envoy_"+metric, "requests_per_second", "instantaneous_rate"
		definition.WindowSeconds = 120
	case "downstream_success_rate":
		definition.ID, definition.Unit, definition.Aggregation = "envoy_downstream_non_5xx_ratio", "ratio", "non_5xx_ratio"
		definition.WindowSeconds = 120
	case "upstream_success_rate":
		definition.ID, definition.Unit, definition.Aggregation = "envoy_upstream_non_5xx_ratio", "ratio", "non_5xx_ratio"
		definition.WindowSeconds = 120
	case "auth_requests":
		definition.ID, definition.Unit, definition.Aggregation = "authorizer_http_requests", "requests", "counter_increase"
	default:
		return MetricDefinition{}, ErrDimension
	}
	return definition, nil
}
