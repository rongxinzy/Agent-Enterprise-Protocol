package httpapi

import (
	"strings"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/app"
)

const (
	sessionClientNameMaxLength     = 64
	sessionClientVersionMaxLength  = 64
	sessionClientDeviceIDMaxLength = 128
)

// sessionClientInput is the optional self-reported client identity accepted on
// login and heartbeat requests. Field bounds mirror the ClientIdentity contract
// schema.
type sessionClientInput struct {
	Name     string `json:"name"`
	Version  string `json:"version"`
	DeviceID string `json:"deviceId"`
}

func (input *sessionClientInput) valid() bool {
	return len(input.Name) >= 1 && len(input.Name) <= sessionClientNameMaxLength &&
		len(input.Version) <= sessionClientVersionMaxLength &&
		len(input.DeviceID) <= sessionClientDeviceIDMaxLength
}

func (input *sessionClientInput) identity() app.SessionClient {
	return app.SessionClient{
		Name:     nonEmptyStringPointer(input.Name),
		Version:  nonEmptyStringPointer(input.Version),
		DeviceID: nonEmptyStringPointer(input.DeviceID),
	}
}

// sessionClient resolves the identity recorded for a newly issued session: an
// explicit request value wins; otherwise a conservative User-Agent label is
// derived. Unknown agents stay unidentified rather than mislabeled.
func sessionClient(input *sessionClientInput, userAgent string) app.SessionClient {
	if input != nil {
		return input.identity()
	}
	return userAgentClient(userAgent)
}

// userAgentClient maps only unambiguous User-Agent shapes to a coarse label.
// Anything else returns a zero identity so the columns stay NULL.
func userAgentClient(userAgent string) app.SessionClient {
	lowered := strings.ToLower(strings.TrimSpace(userAgent))
	name := ""
	switch {
	case strings.Contains(lowered, "electron/"):
		// Electron shells also carry a Mozilla/Chrome prefix, so this check
		// must run before the browser check.
		name = "electron"
	case strings.HasPrefix(lowered, "mozilla/"):
		name = "browser"
	case strings.HasPrefix(lowered, "undici"), lowered == "node", strings.HasPrefix(lowered, "node/"):
		// undici is the fetch implementation behind Node, so both spellings
		// mean a non-browser JavaScript client.
		name = "node"
	case strings.HasPrefix(lowered, "curl/"):
		name = "curl"
	}
	if name == "" {
		return app.SessionClient{}
	}
	return app.SessionClient{Name: nonEmptyStringPointer(name)}
}

// sessionClientJSON renders the stored identity for the admin session list,
// with null when nothing is known and empty optional fields omitted.
func sessionClientJSON(client app.SessionClient) map[string]any {
	if client.Name == nil {
		return nil
	}
	rendered := map[string]any{"name": *client.Name}
	if client.Version != nil {
		rendered["version"] = *client.Version
	}
	if client.DeviceID != nil {
		rendered["deviceId"] = *client.DeviceID
	}
	return rendered
}

func nonEmptyStringPointer(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func pgTextPointer(value pgtype.Text) *string {
	if !value.Valid {
		return nil
	}
	return &value.String
}
