package assettypes

import "testing"

func TestRegistryLookup(t *testing.T) {
	definition, ok := Get("ssh_server")
	if !ok {
		t.Fatal("ssh_server should be registered")
	}
	if definition.Category != "device" || !definition.RequiresCredential {
		t.Fatalf("ssh_server definition = %#v", definition)
	}
	if _, ok := Get("robot_arm"); ok {
		t.Fatal("unregistered type should not resolve")
	}
	if !HasCapability("ssh_server", "exec") || HasCapability("ssh_server", "reboot") {
		t.Fatal("HasCapability misreports the ssh_server templates")
	}
	if definitions := List(); len(definitions) == 0 {
		t.Fatal("List() should expose the registry")
	}
}

func TestValidateSSHServerSpec(t *testing.T) {
	valid := map[string]any{"host": "10.0.0.5", "port": float64(22), "username": "ops", "authMethod": "private_key"}
	if err := ValidateSpec("ssh_server", valid); err != nil {
		t.Fatalf("valid spec rejected: %v", err)
	}
	if err := ValidateSpec("ssh_server", nil); err == nil {
		t.Fatal("nil spec should be rejected")
	}
	if err := ValidateSpec("robot_arm", map[string]any{}); err == nil {
		t.Fatal("unknown type should be rejected")
	}
	for name, spec := range map[string]map[string]any{
		"missing host":     {"username": "ops", "authMethod": "password"},
		"host not string":  {"host": 5, "username": "ops", "authMethod": "password"},
		"host with space":  {"host": "10.0 0.5", "username": "ops", "authMethod": "password"},
		"port too large":   {"host": "h", "port": float64(70000), "username": "ops", "authMethod": "password"},
		"port fractional":  {"host": "h", "port": 22.5, "username": "ops", "authMethod": "password"},
		"missing username": {"host": "h", "authMethod": "password"},
		"bad auth method":  {"host": "h", "username": "ops", "authMethod": "token"},
		"unknown field":    {"host": "h", "username": "ops", "authMethod": "password", "bastion": "x"},
	} {
		if err := ValidateSpec("ssh_server", spec); err == nil {
			t.Errorf("%s: spec should be rejected", name)
		}
	}
	// The port is optional; the rest of the shape still applies.
	if err := ValidateSpec("ssh_server", map[string]any{"host": "h", "username": "ops", "authMethod": "password"}); err != nil {
		t.Fatalf("spec without port rejected: %v", err)
	}
}
