// This test-only entry point renders the production native plugin resources
// for the isolated Compose enforcement regression. Go excludes testdata from
// normal package discovery and production builds.
package main

import (
	"encoding/json"
	"os"

	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/gateway-reconciler/internal/reconciler"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/internal/gatewaypolicy"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/internal/gatewayrender"
)

func main() {
	var input struct {
		Desired         reconciler.DesiredState            `json:"desired"`
		Items           []gatewaypolicy.Limit              `json:"items"`
		Native          *gatewayrender.NativeGatewayConfig `json:"native"`
		PrivateQuota    bool                               `json:"privateQuota"`
		AdminCredential string                             `json:"adminCredential"`
	}
	if err := json.NewDecoder(os.Stdin).Decode(&input); err != nil {
		panic(err)
	}
	publication := gatewaypolicy.Publication{Items: input.Items, Revision: gatewaypolicy.Revision(input.Items)}
	config := gatewayrender.NativeGatewayConfig{Enabled: true, RedisService: "redis.dns", RedisPort: 6379}
	if input.Native != nil {
		config = *input.Native
	}
	config.PrivateQuotaListener = input.PrivateQuota
	credential := "disposable-native-e2e-admin"
	if input.AdminCredential != "" {
		credential = input.AdminCredential
	}
	renderInput := gatewayrender.Input{Deployment: input.Desired.TenantID(), Revision: input.Desired.Revision, Routes: input.Desired.Routes}
	resources, err := gatewayrender.RenderGatewayLimits(renderInput, publication, config, "")
	if err != nil {
		panic(err)
	}
	quota, err := gatewayrender.RenderGatewayQuota(renderInput, config, "", credential)
	if err != nil {
		panic(err)
	}
	resources = append(resources, quota...)
	if err := json.NewEncoder(os.Stdout).Encode(resources); err != nil {
		panic(err)
	}
}
