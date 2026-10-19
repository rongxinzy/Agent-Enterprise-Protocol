// Package assettypes holds the built-in asset type registry. A type defines
// the connection-parameter shape of its assets (validated here in Go, stored
// as JSON), the capability templates with risk and undertaking levels, and
// whether operating the asset requires a stored credential. New access
// methods register in this package without changes to the core asset tables.
package assettypes

import (
	"errors"
	"fmt"
	"sort"
)

const (
	KindQuery  = "query"
	KindAction = "action"
	KindEvent  = "event"

	RiskLow      = "low"
	RiskMedium   = "medium"
	RiskHigh     = "high"
	RiskCritical = "critical"

	LevelStandardInterface = "L1"
	LevelEdgeNode          = "L2"
	LevelUIOperation       = "L3"
	LevelHumanFallback     = "L4"
)

// Capability is one executable or subscribable ability of an asset type.
// Risk and undertaking level live on the capability, not the asset: the same
// switch may expose a low-risk status query and a human-gated configuration
// change.
type Capability struct {
	Key         string `json:"key"`
	Kind        string `json:"kind"`
	Risk        string `json:"risk"`
	Level       string `json:"level"`
	Connector   string `json:"connector"`
	Description string `json:"description"`
}

// Definition describes one asset type. ValidateSpec enforces the structural
// shape of the type-specific connection parameters; the registry never
// touches credentials or network endpoints.
type Definition struct {
	ID                 string        `json:"id"`
	Category           string        `json:"category"`
	DisplayName        string        `json:"displayName"`
	Description        string        `json:"description"`
	RequiresCredential bool          `json:"requiresCredential"`
	Capabilities       []Capability  `json:"capabilities"`
	validateSpec       func(spec map[string]any) error
}

var registry = map[string]Definition{
	"ssh_server": {
		ID:                 "ssh_server",
		Category:           "device",
		DisplayName:        "SSH 服务器",
		Description:        "可通过 SSH 连接执行命令与传输文件的服务器资产",
		RequiresCredential: true,
		Capabilities: []Capability{
			{Key: "exec", Kind: KindAction, Risk: RiskMedium, Level: LevelStandardInterface, Connector: "ssh", Description: "在服务器上执行一条命令并返回输出"},
			{Key: "sftp_read", Kind: KindAction, Risk: RiskLow, Level: LevelStandardInterface, Connector: "ssh", Description: "读取服务器上的文件内容"},
			{Key: "sftp_write", Kind: KindAction, Risk: RiskHigh, Level: LevelStandardInterface, Connector: "ssh", Description: "写入或替换服务器上的文件"},
		},
		validateSpec: validateSSHServerSpec,
	},
}

// Get returns the definition of a registered asset type.
func Get(id string) (Definition, bool) {
	definition, ok := registry[id]
	return definition, ok
}

// List returns every registered type ordered by ID.
func List() []Definition {
	definitions := make([]Definition, 0, len(registry))
	for _, definition := range registry {
		definitions = append(definitions, definition)
	}
	sort.Slice(definitions, func(i, j int) bool { return definitions[i].ID < definitions[j].ID })
	return definitions
}

// ValidateSpec checks type-specific connection parameters. An empty spec is
// rejected: the shape is part of the type contract.
func ValidateSpec(typeID string, spec map[string]any) error {
	if spec == nil {
		return errors.New("spec is required")
	}
	definition, ok := registry[typeID]
	if !ok {
		return fmt.Errorf("unknown asset type %q", typeID)
	}
	return definition.validateSpec(spec)
}

// HasCapability reports whether the key belongs to the type's templates.
func HasCapability(typeID, key string) bool {
	definition, ok := registry[typeID]
	if !ok {
		return false
	}
	for _, capability := range definition.Capabilities {
		if capability.Key == key {
			return true
		}
	}
	return false
}

func validateSSHServerSpec(spec map[string]any) error {
	for field := range spec {
		switch field {
		case "host", "port", "username", "authMethod":
		default:
			return fmt.Errorf("spec.%s is not a recognized ssh_server field", field)
		}
	}
	host, ok := spec["host"].(string)
	if !ok || host == "" {
		return errors.New("spec.host is required")
	}
	if len(host) > 253 || containsSpace(host) {
		return errors.New("spec.host must be a hostname or IP without whitespace")
	}
	if port, present := spec["port"]; present {
		number, ok := port.(float64)
		if !ok || number != float64(int(number)) || number < 1 || number > 65535 {
			return errors.New("spec.port must be an integer between 1 and 65535")
		}
	}
	username, ok := spec["username"].(string)
	if !ok || username == "" {
		return errors.New("spec.username is required")
	}
	if len(username) > 64 {
		return errors.New("spec.username must be at most 64 characters")
	}
	method, ok := spec["authMethod"].(string)
	if !ok || (method != "password" && method != "private_key") {
		return errors.New("spec.authMethod must be password or private_key")
	}
	return nil
}

func containsSpace(value string) bool {
	for _, r := range value {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			return true
		}
	}
	return false
}
