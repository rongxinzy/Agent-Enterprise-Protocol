package modelpricing

import (
	"strings"
	"testing"
)

func TestReferencePriceValidation(t *testing.T) {
	base := Configuration{Currency: "CNY", InputPricePerMillionTokens: "0.000001", OutputPricePerMillionTokens: "0", Source: strings.Repeat("价", 200)}
	if !base.Valid() {
		t.Fatal("valid exact prices or Unicode source rejected")
	}
	cached := "999999999.999999"
	base.CachedInputPricePerMillionTokens = &cached
	if !base.Valid() {
		t.Fatal("bounded cached input price rejected")
	}
	for _, price := range []string{"", "-1", "1e3", "01", ".1", "1.", "NaN", "0.0000001", "1000000000"} {
		candidate := base
		candidate.InputPricePerMillionTokens = price
		if candidate.Valid() {
			t.Fatalf("invalid price accepted: %q", price)
		}
	}
	base.Source += "价"
	if base.Valid() {
		t.Fatal("oversized source accepted")
	}
}
