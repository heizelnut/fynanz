package sync

import (
	"fmt"
	"strconv"
	"strings"
)

// Amounts arrive as decimal strings ("1.23", "-45.00") with per-currency
// precision. We store signed integer minor units (expenses negative) so
// aggregation never touches floats.

// currencyScale maps ISO 4217 currency to its minor-unit exponent.
// Anything not listed defaults to 2.
var currencyScale = map[string]int{
	// 0 decimals
	"BIF": 0, "CLP": 0, "DJF": 0, "GNF": 0, "ISK": 0, "JPY": 0, "KMF": 0,
	"KRW": 0, "PYG": 0, "RWF": 0, "UGX": 0, "VND": 0, "VUV": 0, "XAF": 0,
	"XOF": 0, "XPF": 0,
	// 3 decimals
	"BHD": 3, "IQD": 3, "JOD": 3, "KWD": 3, "LYD": 3, "OMR": 3, "TND": 3,
}

func scaleForCurrency(currency string) int {
	if s, ok := currencyScale[strings.ToUpper(currency)]; ok {
		return s
	}
	return 2
}

// amountToMinor converts a decimal string into minor units for the currency.
// Trailing digits beyond the currency's precision are truncated (bank
// amounts are validated for precision upstream, so sign changes never happen
// here).
func amountToMinor(amount, currency string) (int64, error) {
	scale := scaleForCurrency(currency)
	raw := strings.TrimSpace(amount)
	if raw == "" {
		return 0, fmt.Errorf("money: empty amount")
	}
	negative := strings.HasPrefix(raw, "-")
	raw = strings.TrimPrefix(strings.TrimPrefix(raw, "-"), "+")

	parts := strings.SplitN(raw, ".", 2)
	intPart := parts[0]
	fracPart := ""
	if len(parts) == 2 {
		fracPart = parts[1]
	}
	if len(fracPart) > scale {
		fracPart = fracPart[:scale]
	}
	fracPart = fracPart + strings.Repeat("0", scale-len(fracPart))
	if intPart == "" {
		intPart = "0"
	}
	digits := intPart + fracPart
	if len(digits) > 18 { // int64 safety (9e18)
		return 0, fmt.Errorf("money: amount %q too large", amount)
	}
	n, err := strconv.ParseInt(digits, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("money: parse %q: %w", amount, err)
	}
	if negative {
		n = -n
	}
	return n, nil
}

// normalizeAmount enforces the storage convention: DBIT (expense) negative,
// CRDT (income) positive, whatever sign the bank sent.
func normalizeAmount(amount Amount, creditDebit string) (int64, error) {
	minor, err := amountToMinor(amount.Amount, amount.Currency)
	if err != nil {
		return 0, err
	}
	switch creditDebit {
	case "DBIT":
		if minor > 0 {
			minor = -minor
		}
	case "CRDT":
		if minor < 0 {
			minor = -minor
		}
	}
	return minor, nil
}

// FormatMinor renders minor units as a plain decimal string ("-12.34") using
// the currency's scale.
func FormatMinor(minor int64, currency string) string {
	scale := scaleForCurrency(currency)
	neg := minor < 0
	if neg {
		minor = -minor
	}
	s := fmt.Sprintf("%0*d", scale+1, minor) // ensure >= scale+1 digits
	ip, fp := s[:len(s)-scale], s[len(s)-scale:]
	out := ip
	if scale > 0 {
		out += "." + fp
	}
	if neg {
		out = "-" + out
	}
	return out
}
