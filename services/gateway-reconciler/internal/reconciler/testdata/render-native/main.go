// This test-only entry point renders the production native plugin resources
// for the isolated Compose enforcement regression. Go excludes testdata from
// normal package discovery and production builds.
package main

import (
	"encoding/json"
	"os"

	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/gateway-reconciler/internal/reconciler"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/internal/gatewaypolicy"
)

func main() {
	var input struct {
		Desired reconciler.DesiredState `json:"desired"`
		Items   []gatewaypolicy.Limit   `json:"items"`
	}
	if err := json.NewDecoder(os.Stdin).Decode(&input); err != nil {
		panic(err)
	}
	publication := gatewaypolicy.Publication{Items: input.Items, Revision: gatewaypolicy.Revision(input.Items)}
	config := reconciler.NativeGatewayConfig{Enabled: true, RedisService: "redis.dns", RedisPort: 6379}
	resources, err := reconciler.RenderGatewayLimits(input.Desired, publication, config, "")
	if err != nil {
		panic(err)
	}
	quota, err := reconciler.RenderGatewayQuota(input.Desired, config, "", "disposable-native-e2e-admin")
	if err != nil {
		panic(err)
	}
	resources = append(resources, quota...)
	if err := json.NewEncoder(os.Stdout).Encode(resources); err != nil {
		panic(err)
	}
}
