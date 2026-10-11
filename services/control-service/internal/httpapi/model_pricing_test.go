package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestModelPricingWriteExactPricesAndClear(t *testing.T) {
	for _, body := range []string{
		`{"pricing":{"currency":"CNY","inputPricePerMillionTokens":"0.000001","outputPricePerMillionTokens":"0","source":"参考价格"},"expectedVersion":0}`,
		`{"pricing":null,"expectedVersion":5}`,
	} {
		var value modelPricingWrite
		if err := json.Unmarshal([]byte(body), &value); err != nil || !validModelPricingWrite(value) {
			t.Fatalf("valid price rejected: %s, %v", body, err)
		}
	}
	for _, body := range []string{
		`{}`, `{"pricing":null}`, `{"expectedVersion":0}`, `{"pricing":null,"expectedVersion":-1}`,
		`{"pricing":null,"expectedVersion":9007199254740992}`,
		`{"pricing":{"currency":"CNY","inputPricePerMillionTokens":1,"outputPricePerMillionTokens":"2"},"expectedVersion":0}`,
		`{"pricing":{"currency":"CNY","inputPricePerMillionTokens":"-1","outputPricePerMillionTokens":"2"},"expectedVersion":0}`,
		`{"pricing":{"currency":"CNY","inputPricePerMillionTokens":"1e3","outputPricePerMillionTokens":"2"},"expectedVersion":0}`,
		`{"pricing":{"currency":"CNY","inputPricePerMillionTokens":"0.0000001","outputPricePerMillionTokens":"2"},"expectedVersion":0}`,
		`{"pricing":{"currency":"CNY","inputPricePerMillionTokens":"1000000000","outputPricePerMillionTokens":"2"},"expectedVersion":0}`,
		`{"pricing":{"currency":"cny","inputPricePerMillionTokens":"1","outputPricePerMillionTokens":"2"},"expectedVersion":0}`,
		`{"pricing":{"currency":"CNY","inputPricePerMillionTokens":"1","outputPricePerMillionTokens":"2","apiKey":"unexpected"},"expectedVersion":0}`,
		`{"pricing":{"currency":"CNY","inputPricePerMillionTokens":"1","outputPricePerMillionTokens":"2","cachedInputPricePerMillionTokens":""},"expectedVersion":0}`,
		`{"pricing":{"currency":"CNY","inputPricePerMillionTokens":"1","outputPricePerMillionTokens":"2","cachedInputPricePerMillionTokens":null},"expectedVersion":0}`,
		`{"pricing":{"currency":"CNY","inputPricePerMillionTokens":"1","outputPricePerMillionTokens":"2","source":null},"expectedVersion":0}`,
	} {
		var value modelPricingWrite
		if json.Unmarshal([]byte(body), &value) == nil && validModelPricingWrite(value) {
			t.Fatalf("invalid price accepted: %s", body)
		}
	}
}

func TestModelPricingPermissions(t *testing.T) {
	for method, want := range map[string]string{http.MethodGet: "models.read", http.MethodPut: "models.write"} {
		actual := requiredAdminPermission(method, "/aep/v1/admin/models/chat/pricing")
		if len(actual) != 1 || actual[0] != want {
			t.Fatalf("%s: %v", method, actual)
		}
	}
}
