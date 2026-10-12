// Package modelpricing defines reference prices, never runtime cost calculations.
package modelpricing

import (
	"regexp"
	"unicode/utf8"
)

const MaxVersion int64 = 9007199254740991

var pricePattern = regexp.MustCompile(`^(0|[1-9][0-9]{0,8})(\.[0-9]{1,6})?$`)
var currencyPattern = regexp.MustCompile(`^[A-Z]{3}$`)

type Configuration struct {
	Currency                         string  `json:"currency"`
	InputPricePerMillionTokens       string  `json:"inputPricePerMillionTokens"`
	OutputPricePerMillionTokens      string  `json:"outputPricePerMillionTokens"`
	CachedInputPricePerMillionTokens *string `json:"cachedInputPricePerMillionTokens,omitempty"`
	Source                           string  `json:"source,omitempty"`
}

func (c Configuration) Valid() bool {
	return currencyPattern.MatchString(c.Currency) &&
		pricePattern.MatchString(c.InputPricePerMillionTokens) &&
		pricePattern.MatchString(c.OutputPricePerMillionTokens) &&
		(c.CachedInputPricePerMillionTokens == nil || pricePattern.MatchString(*c.CachedInputPricePerMillionTokens)) &&
		utf8.RuneCountInString(c.Source) <= 200
}
