package categorize

import "testing"

func TestNormalizeMerchant(t *testing.T) {
	cases := map[string]string{
		"":                                "",
		"  S Market  ":                    "s market",
		"Ella Hämäläinen-CRDT-6.94-f0ao3": "ella hämäläinen",
		"Ella Hämäläinen-DBIT-4.47-snjus": "ella hämäläinen",
		"ACME-CRDT-100.00-xyz":            "acme",
		"NETFLIX.COM":                     "netflix.com",
		"  a   b   c  ":                   "a b c",
		"Amazon - CRDT - 5.00 - ab12":     "amazon", // spaces around artifacts
	}
	for in, want := range cases {
		if got := NormalizeMerchant(in); got != want {
			t.Errorf("NormalizeMerchant(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestClassifyExact(t *testing.T) {
	rules := []Rule{
		{ID: 1, CategoryID: 10, Pattern: "s market", MatchType: "exact"},
		{ID: 2, CategoryID: 11, Pattern: "NETFLIX.COM", MatchType: "exact"},
		{ID: 3, CategoryID: 12, Pattern: "acme", MatchType: "contains"},
	}
	cases := []struct {
		merchant  string
		wantID    int64
		wantFound bool
	}{
		{"s market", 10, true},
		{"netflix.com", 11, true},
		{"my acme corp ltd", 12, true},
		{"s-market", 0, false},
		{"", 0, false},
		{"some supermarket", 0, false},
	}
	for _, c := range cases {
		id, found := Classify(rules, c.merchant)
		if found != c.wantFound || id != c.wantID {
			t.Errorf("Classify(%q) = (%d, %v), want (%d, %v)", c.merchant, id, found, c.wantID, c.wantFound)
		}
	}
}

func TestIsInternalTransfer(t *testing.T) {
	own := []OwnAccount{
		{Name: "Ella Nieminen", Iban: "FI1234567890"},
		{Name: "Aino Nieminen", Iban: "FI0987654321"},
	}
	cases := []struct {
		name, acc string
		want      bool
	}{
		{"", "FI1234567890", true},          // by IBAN (exact)
		{"", "fi 12 34567890", true},        // IBAN with spaces, lowercase
		{"Ella Nieminen", "", true},         // by name
		{"ella nieminen", "", true},         // name case-insensitive
		{"S Market", "DE9999999999", false}, // external
		{"", "", false},
	}
	for _, c := range cases {
		if got := IsInternalTransfer(c.name, c.acc, own); got != c.want {
			t.Errorf("IsInternalTransfer(%q, %q) = %v, want %v", c.name, c.acc, got, c.want)
		}
	}
}
