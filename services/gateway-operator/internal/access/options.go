// Package access projects session grants into native Authorino configuration.
// It implements no request evaluator, credential verifier or result cache.
package access

import (
	"errors"
	"net/url"
	"strings"
)

var ErrInput = errors.New("invalid native access projection")
var ErrOwnership = errors.New("native access ownership or publication conflict")
var ErrSuperseded = errors.New("access projection superseded")

// Options are trusted operator deployment configuration, never request input.
// PolicyBaseURL must serve the projected ConfigMap files using a reviewed
// static server. This controller does not supply a policy HTTP server.
type Options struct {
	IssuerURL          string
	PolicyBaseURL      string
	RequireEntitlement bool
	RefreshSeconds     int64
}

func (o Options) Validate() error {
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
