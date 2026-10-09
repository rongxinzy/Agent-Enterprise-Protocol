package gatewaysource

import (
	"net/url"
	"regexp"
	"strconv"
	"time"
)

// LogMetricQuery delegates organization aggregates to Loki's native range
// engine over metadata-only gateway logs. Vector's native unnest transform
// creates one stream per call-time membership; no numeric data is calculated
// by the collector, AEP or the client. Base streams are never double counted.
func LogMetricQuery(tenant string, values url.Values) (url.Values, error) {
	start, end, step, err := Window(values)
	if err != nil {
		return nil, err
	}
	group := values.Get("groupBy")
	dimension, label := "base", ""
	switch group {
	case "", "none":
	case "model":
		label = "model_id"
	case "route":
		label = "route"
	case "user":
		label = "user_id"
	case "team":
		dimension, label = "team", "team_id"
	case "role":
		dimension, label = "role", "role_id"
	default:
		return nil, ErrDimension
	}
	selector := `{aep_source="gateway",aep_deployment_id=` + strconv.Quote(tenant) + `,aep_dimension=` + strconv.Quote(dimension) + `} | json`
	for key, field := range map[string]string{"modelId": "model_id", "userId": "user_id"} {
		if value := values.Get(key); value != "" {
			selector += " | " + field + "=" + strconv.Quote(value)
		}
	}
	for key, field := range map[string]string{"teamId": "team_ids", "roleId": "role_ids"} {
		if value := values.Get(key); value != "" {
			selector += " | " + field + "=~" + strconv.Quote(".*"+regexp.QuoteMeta("|"+encodeID(value)+"|")+".*")
		}
	}
	agg := func(query string) string {
		if label == "" {
			return "sum(" + query + ")"
		}
		return "sum by (" + label + ")(" + query + ")"
	}
	rangePart := "[" + strconv.Itoa(step) + "s]"
	count := func(filter string) string {
		return agg("count_over_time(" + selector + filter + ` | __error__="" ` + rangePart + ")")
	}
	var query string
	switch values.Get("metric") {
	case "calls":
		query = count("")
	case "failures":
		query = count(` | status >= 400`)
	case "input_tokens", "output_tokens":
		field := "input_token"
		if values.Get("metric") == "output_tokens" {
			field = "output_token"
		}
		query = agg("sum_over_time(" + selector + " | unwrap " + field + ` | __error__="" ` + rangePart + ")")
	case "service_duration", "first_token_duration":
		field := "llm_service_duration"
		if values.Get("metric") == "first_token_duration" {
			field = "llm_first_token_duration"
		}
		query = agg("sum_over_time("+selector+" | unwrap "+field+` | __error__="" `+rangePart+")") + " / " + count(" | "+field+`!="" | `+field+`!="-"`)
	default:
		return nil, ErrDimension
	}
	return url.Values{"query": {query}, "start": {start.Format(time.RFC3339Nano)}, "end": {end.Format(time.RFC3339Nano)}, "step": {strconv.Itoa(step)}}, nil
}
