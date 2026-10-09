// Package gatewaysource transports bounded native monitoring/plugin results.
// It does not collect usage or calculate monitoring values.
package gatewaysource

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var ErrUnavailable = errors.New("gateway source unavailable")

func Consumer(deployment, user string) string {
	return ConsumerPrefix(deployment) + encodeID(user)
}

func encodeID(value string) string { return base64.RawURLEncoding.EncodeToString([]byte(value)) }

func ConsumerPrefix(deployment string) string {
	return "aep." + base64.RawURLEncoding.EncodeToString([]byte(deployment)) + "."
}

// Fetch never forwards browser credentials, follows redirects, or exposes the
// source's errors (which may contain tokens, internal URLs, or model content).
func Fetch(ctx context.Context, endpoint, token, tenant, path, method string, values url.Values) (json.RawMessage, error) {
	base, err := url.Parse(endpoint)
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return nil, ErrUnavailable
	}
	base.Path = strings.TrimRight(base.Path, "/") + path
	var body io.Reader
	if method == http.MethodGet {
		base.RawQuery = values.Encode()
	} else {
		body = strings.NewReader(values.Encode())
	}
	request, err := http.NewRequestWithContext(ctx, method, base.String(), body)
	if err != nil {
		return nil, ErrUnavailable
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	request.Header.Set("X-Scope-OrgID", tenant)
	if method != http.MethodGet {
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	client := &http.Client{Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		return nil, ErrUnavailable
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return nil, ErrUnavailable
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, (4<<20)+1))
	if err != nil || len(data) > 4<<20 || !json.Valid(data) {
		return nil, ErrUnavailable
	}
	var envelope struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil || (envelope.Status != "" && envelope.Status != "success") {
		return nil, ErrUnavailable
	}
	return data, nil
}
