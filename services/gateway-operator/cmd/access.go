package main

import (
	"fmt"
	"strconv"

	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/gateway-operator/internal/access"
)

func accessOptions(getenv func(string) string) (*access.Options, error) {
	value := getenv("AEP_GATEWAY_ACCESS_OPERATOR_ENABLED")
	if value == "" {
		return nil, nil
	}
	enabled, err := strconv.ParseBool(value)
	if err != nil {
		return nil, fmt.Errorf("invalid AEP_GATEWAY_ACCESS_OPERATOR_ENABLED")
	}
	if !enabled {
		return nil, nil
	}
	require := true
	if value := getenv("AEP_GATEWAY_ACCESS_REQUIRE_ENTITLEMENT"); value != "" {
		require, err = strconv.ParseBool(value)
		if err != nil {
			return nil, fmt.Errorf("invalid AEP_GATEWAY_ACCESS_REQUIRE_ENTITLEMENT")
		}
	}
	refresh, err := strconv.ParseInt(getenv("AEP_GATEWAY_ACCESS_REFRESH_SECONDS"), 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid AEP_GATEWAY_ACCESS_REFRESH_SECONDS")
	}
	options := &access.Options{IssuerURL: getenv("AEP_GATEWAY_ACCESS_ISSUER_URL"),
		PolicyBaseURL: getenv("AEP_GATEWAY_ACCESS_POLICY_BASE_URL"), RequireEntitlement: require, RefreshSeconds: refresh}
	if options.Validate() != nil {
		return nil, fmt.Errorf("invalid gateway access operator options")
	}
	return options, nil
}
