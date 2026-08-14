package sync

import "testing"

func TestAmountToMinor(t *testing.T) {
	cases := []struct {
		amount, currency string
		want             int64
		wantErr          bool
	}{
		{"1.23", "EUR", 123, false},
		{"-12.34", "EUR", -1234, false},
		{"0.01", "EUR", 1, false},
		{"10", "EUR", 1000, false},
		{"10.", "EUR", 1000, false},
		{"+7.5", "EUR", 750, false},
		{"1.234", "USD", 123, false}, // truncate beyond scale
		{"5", "JPY", 5, false},       // zero-decimal currency
		{"5.99", "JPY", 5, false},
		{"1.234", "BHD", 1234, false}, // three-decimal currency
		{"", "EUR", 0, true},
		{"abc", "EUR", 0, true},
		{"99999999999999999999", "EUR", 0, true}, // overflow
	}
	for _, c := range cases {
		got, err := amountToMinor(c.amount, c.currency)
		if c.wantErr {
			if err == nil {
				t.Errorf("amountToMinor(%q, %q): expected error, got %d", c.amount, c.currency, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("amountToMinor(%q, %q): %v", c.amount, c.currency, err)
			continue
		}
		if got != c.want {
			t.Errorf("amountToMinor(%q, %q) = %d, want %d", c.amount, c.currency, got, c.want)
		}
	}
}

func TestNormalizeAmountSign(t *testing.T) {
	cases := []struct {
		amount    Amount
		indicator string
		want      int64
	}{
		{Amount{"EUR", "25.00"}, "CRDT", 2500},
		{Amount{"EUR", "-25.00"}, "CRDT", 2500}, // bank sent negative credit
		{Amount{"EUR", "42.50"}, "DBIT", -4250},
		{Amount{"EUR", "-42.50"}, "DBIT", -4250},
	}
	for _, c := range cases {
		got, err := normalizeAmount(c.amount, c.indicator)
		if err != nil {
			t.Errorf("normalizeAmount(%v, %s): %v", c.amount, c.indicator, err)
			continue
		}
		if got != c.want {
			t.Errorf("normalizeAmount(%v, %s) = %d, want %d", c.amount, c.indicator, got, c.want)
		}
	}
}

func TestFormatMinor(t *testing.T) {
	cases := []struct {
		minor    int64
		currency string
		want     string
	}{
		{1234, "EUR", "12.34"},
		{-1234, "EUR", "-12.34"},
		{5, "JPY", "5"}, // zero-decimal currency renders without fraction
		{123456, "BHD", "123.456"},
	}
	for _, c := range cases {
		if got := FormatMinor(c.minor, c.currency); got != c.want {
			t.Errorf("FormatMinor(%d, %q) = %q, want %q", c.minor, c.currency, got, c.want)
		}
	}
}
