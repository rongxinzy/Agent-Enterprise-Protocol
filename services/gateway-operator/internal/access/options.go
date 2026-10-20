// Package access projects grants into native Authorino configuration.
// It implements no request evaluator, credential verifier or result cache.
package access

import (
	"errors"
	"net/url"
	"strings"
	"unicode"

	"k8s.io/apimachinery/pkg/util/validation"
)

var ErrInput = errors.New("invalid native access projection")
var ErrOwnership = errors.New("native access ownership or publication conflict")
var ErrSuperseded = errors.New("access projection superseded")
var ErrRoutePending = errors.New("native access route cleanup pending")

// Options are trusted operator deployment configuration, never request input.
// PolicyBaseURL must serve the projected ConfigMap files using a reviewed
// static server. This controller does not supply a policy HTTP server.
type Options struct {
	IssuerURL          string
	PolicyBaseURL      string
	RequireEntitlement bool
	RefreshSeconds     int64
	// IngressHost opts into session selection routes on a dedicated Higress
	// listener. It is deployment configuration, never a caller-provided host.
	IngressHost string `json:",omitempty"`
	// Empty keeps workload grants disabled. The trusted deployment audience
	// must match the employee's projected token and the native TokenReview.
	WorkloadAudience string `json:",omitempty"`
}

func (o Options) Validate() error {
	if len(o.WorkloadAudience) > 256 || strings.IndexFunc(o.WorkloadAudience, unicode.IsSpace) != -1 ||
		strings.IndexFunc(o.WorkloadAudience, unicode.IsControl) != -1 {
		return ErrInput
	}
	if o.IngressHost != "" && len(validation.IsDNS1123Subdomain(o.IngressHost)) != 0 {
		return ErrInput
	}
	if o.RefreshSeconds < 1 || o.RefreshSeconds > 60 {
		return ErrInput
	}
	for _, value := range []string{o.IssuerURL, o.PolicyBaseURL} {
		u, err := url.Parse(value)
		if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
			(u.Scheme != "http" && u.Scheme != "https") || strings.TrimSpace(value) != value {
			return ErrInput
		}
	}
	return nil
}
