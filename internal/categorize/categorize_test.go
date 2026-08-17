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
		id, found := Classify(rules, c.merchant, "")
		if found != c.wantFound || id != c.wantID {
			t.Errorf("Classify(%q) = (%d, %v), want (%d, %v)", c.merchant, id, found, c.wantID, c.wantFound)
		}
	}
}

func TestClassifyFields(t *testing.T) {
	// Same pattern text, different fields, different categories: the field
	// decides which text each rule looks at.
	rules := []Rule{
		{ID: 1, CategoryID: 20, Pattern: "affitto", Field: FieldDescription, MatchType: MatchFuzzy},
		{ID: 2, CategoryID: 21, Pattern: "affitto", Field: FieldMerchant, MatchType: MatchFuzzy},
		{ID: 3, CategoryID: 22, Pattern: " you ", Field: FieldDescription, MatchType: MatchContains},
	}
	cases := []struct {
		merchant, desc string
		wantID         int64
		wantFound      bool
	}{
		// Description rule fires on the description; the merchant rule only
		// on the merchant text.
		{"spesa", "pagamento affitto luglio", 20, true},
		{"agente affitto", "canone", 21, true},
		{"", "affitto", 20, true},
		// The description rule fires on description text even when the
		// merchant is empty; the merchant rule cannot see description text.
		{"", "agente affitto", 20, true},
		{"affitto", "", 21, true},
		// contains ignores punctuation/whitespace via normalization.
		{"", "hey you! foo", 22, true},
		{"", "hey! foo", 0, false},
		// empty everything: nothing matches.
		{"", "", 0, false},
	}
	for _, c := range cases {
		id, found := Classify(rules, c.merchant, c.desc)
		if found != c.wantFound || id != c.wantID {
			t.Errorf("Classify(%q, %q) = (%d, %v), want (%d, %v)", c.merchant, c.desc, id, found, c.wantID, c.wantFound)
		}
	}
}

func TestFuzzyMatch(t *testing.T) {
	cases := []struct {
		pattern, target string
		want            bool
	}{
		// plain word containment, order irrelevant.
		{"centrale mercato", "mercato centrale", true},
		{"amazon", "amazon prime video", true},
		{"amazon prime video", "amazon", false},
		// punctuation/hyphens split words: "s market" matches "S-Market HK".
		{"s market", "S-Market HK", true},
		// stopword-short pattern words are skipped, but the pattern must
		// still contain at least one meaningful word.
		{"il", "il sole sorge", false},
		{"affitto", "affitto luglio", true},
		// one-typo tolerance on longer words.
		{"affito", "affitto luglio", true},     // single insertion
		{"AMAZON PRIME", "amazin prime", true}, // substitution, both ≥4
		{"amazon", "amazonia", false},          // edit distance 2: no typo tolerance
		// 3-letter words match exactly only: "pay" must not swallow "day".
		{"pay", "day", false},
		{"pay", "pay day", true},
		// empty sides never match.
		{"", "anything", false},
		{"affitto", "", false},
	}
	for _, c := range cases {
		if got := FuzzyMatch(c.pattern, c.target); got != c.want {
			t.Errorf("FuzzyMatch(%q, %q) = %v, want %v", c.pattern, c.target, got, c.want)
		}
	}
}

// TestIsP2P locks the detector against the shapes seen in a live Banca
// Mediolanum feed (401 transactions): the connector populates neither MCC
// nor bank transaction codes, so everything rides on the narration.
func TestIsP2P(t *testing.T) {
	cases := []struct {
		name   string
		signal P2PSignal
		want   bool
	}{
		// From/To reference segments: the friend-transfer pattern (money in
		// "From X", money out "To X", names often end in a letter initial).
		{"friend in: From + initial", P2PSignal{Merchant: "riccardo conte", Narrative: "Yotube Premium | From Riccardo C"}, true},
		{"friend in: reference + From", P2PSignal{Merchant: "giulia rollo", Narrative: "Acqua me e Fede | From Giulia R"}, true},
		{"friend out: reference + To", P2PSignal{Merchant: "simone passione", Narrative: "dubbirregrazzie | To Simone Passione"}, true},
		{"friend out: all caps", P2PSignal{Merchant: "vito pio prete", Narrative: "To VITO PIO PRETE"}, true},
		{"friend in: transfer-from prefix", P2PSignal{Merchant: "mario rossi", Narrative: "Transfer from Mario Rossi"}, true},
		{"from/to segment after a for-reference", P2PSignal{Merchant: "vito pio prete", Narrative: "For TicketSms | From Vito Pio P"}, true},
		// Full SEPA credit to a person-shaped counterparty.
		{"sepa person credit", P2PSignal{Merchant: "savina andrea", Narrative: "SAVINA ANDREA | Note: versamento | Data Regolamento: 07/08/26 | Cod.Id.Ord: IT33 W076 | BONIFICO A VOSTRO FAVORE"}, true},
		// Never P2P on this feed: card payments, direct debits, self, payroll.
		{"mediolanum card payment", P2PSignal{Merchant: "del 27/07/26", Narrative: "DEL 27/07/26 | Valuta EUR Paese Italia | C/O CIARELLI SRL LANCIANO | CARTA N. 537572******8854 - CIRCUITO MASTERCARD | Cod. MCC 7311 | Causale Movimento: 01"}, false},
		{"revolut top-up via own card", P2PSignal{Merchant: "del 27/07/26", Narrative: "DEL 27/07/26 | Valuta EUR Paese Irlanda | C/O Revolut**7383* Dublin | CARTA N. 537572******8854 - CIRCUITO MASTERCARD | Cod. MCC 4829 | Causale Movimento: 01"}, false},
		{"direct debit", P2PSignal{Merchant: "core rcur prg.car.: 262261116011209", Narrative: "CORE RCUR Prg.Car.: 262261116011209 | 4LA2225AVSKZS PayPal Europe S.a.r.l. et Cie S.C.A | Causale Movimento: TE | Addebito Diretto"}, false},
		{"top-up by own card", P2PSignal{Merchant: "top-up by *8854", Narrative: "Top-Up by *8854"}, false},
		{"payroll", P2PSignal{Merchant: "dedalus italia spa", Narrative: "DEDALUS ITALIA SPA | Note: STIPENDIO MESE | Data Regolamento: 01/07/26 | Cod.Id.Ord: IT49"}, false},
		{"company refund with sepa markers", P2PSignal{Merchant: "google ireland limited", Narrative: "Google Ireland Limited | Note: GG104EWAN0 | Data Regolamento: 16/06/26 | Cod.Id.Ord: IE64"}, false},
		{"debit movement code rows", P2PSignal{Merchant: "causale movimento: (6", Narrative: "Causale Movimento: (6 | Commissioni pagam. paesi NON UE"}, false},
		{"crypto exchange", P2PSignal{Merchant: "exchanged to usdc", Narrative: "Exchanged to USDC"}, false},
		{"cc pull", P2PSignal{Merchant: "cc pull 12408", Narrative: "Cc Pull 12408"}, false},
		// Bare-name rows are structurally identical on this feed (merchants
		// like "Focacciami" and friends like "Calcagnile Eleonora" narrate
		// exactly their name) — deliberately NOT flagged.
		{"bare merchant name", P2PSignal{Merchant: "focacciami", Narrative: "Focacciami"}, false},
		{"bare two-word merchant", P2PSignal{Merchant: "villanova minoia", Narrative: "Villanova Minoia"}, false},
		{"bare friend name (documented gap)", P2PSignal{Merchant: "calcagnile eleonora", Narrative: "Calcagnile Eleonora"}, false},
		// A raw MCC always wins (card/POS payment).
		{"mcc present kills from/to", P2PSignal{MerchantCategoryCode: "5411", Merchant: "mario rossi", Narrative: "From Mario Rossi"}, false},
		// Emptiness and sandbox/mock shapes.
		{"empty signal", P2PSignal{}, false},
		{"sandbox merchant", P2PSignal{Merchant: "s market", Narrative: "Grocery run"}, false},
		{"sandbox artifact income", P2PSignal{Merchant: "ella hämäläinen", Narrative: ""}, false},
	}
	for _, c := range cases {
		if got := IsP2P(c.signal); got != c.want {
			t.Errorf("%s: IsP2P(%+v) = %v, want %v", c.name, c.signal, got, c.want)
		}
	}
}

func TestReferenceForRule(t *testing.T) {
	cases := map[string]string{
		"spritz | From Mario Rossi":                        "spritz",
		"From Riccardo C":                                  "",
		"SAVINA ANDREA | Note: versamento | Cod.Id.Ord: X": "savina andrea",
		"": "",
	}
	for in, want := range cases {
		if got := ReferenceForRule(in); got != want {
			t.Errorf("ReferenceForRule(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormalizeDescription(t *testing.T) {
	cases := map[string]string{
		"":                    "",
		"  Affitto   Luglio ": "affitto luglio",
		// Unlike merchants, descriptions never lose their trailer.
		"Dinner-CRDT-4.47-snjus": "dinner-crdt-4.47-snjus",
	}
	for in, want := range cases {
		if got := NormalizeDescription(in); got != want {
			t.Errorf("NormalizeDescription(%q) = %q, want %q", in, got, want)
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
