package categorize

import (
	"regexp"
	"strings"
)

// P2PSignal carries the fields used to recognise a person-to-person transfer
// (friend/family) versus a merchant payment (AGENTS.md §4/§5).
//
// Tuned against a live Banca Mediolanum feed (401 real transactions) where
// the connector leaves merchant_category_code and bank_transaction_code
// empty for most rows — the discriminating text lives in the remittance
// narration. Friends there transfer with "<shared expense> | From <Name>"
// (money in) / "<...> | To <Name>" (money out) references, or as full SEPA
// credits ending in "BONIFICO A VOSTRO FAVORE / Cod.Id.Ord:" when the
// counterparty name looks like a person. Card payments always carry "CARTA
// N. ... CIRCUITO ... Cod. MCC" (plus a "DEL ... | C/O ..." structure) or a
// raw MCC; direct debits "ADDEBITO DIRETTO" / "CORE RCUR"; self top-ups
// "Top-Up by *xxxx".
//
// Bare counterparty rows — "Calcagnile Eleonora" vs "Focacciami",
// "Villanova Minoia" vs "Quarta Carmine Francesca" — are structurally
// identical on this feed, so they are NOT flagged (they keep behaving as
// merchants and stay rule-matchable). Only explicit transfer signals force a
// row into the inbox.
type P2PSignal struct {
	MerchantCategoryCode string // raw MCC (card/POS payments); may be empty
	Merchant             string // counterparty name (raw or normalized)
	Narrative            string // remittance information + bank note
}

// fromToRe matches a narration segment like "From Riccardo C", "To VITO PIO
// PRETE" or "Transfer from Mario Rossi" — the payer/payee reference friends
// write on transfers. Names may end in a single-letter initial ("Giulia R"),
// which is why name words may be one character; IsP2P then requires a
// multi-word name (or a standalone name of ≥2 chars) before trusting it.
var fromToRe = regexp.MustCompile(
	`(?i)^(?:(?:transfer|payment|bonifico|pagamento|giro|sepa|istan|istapay|instant)\s+)?` +
		`(?:from|to)\s+([a-zà-ÿ]{1,}(?:\s+[a-zà-ÿ]{1,}){0,3})$`)

// p2pExcludes are substrings of the (lowercased) merchant+narrative that
// prove the movement is NOT person-to-person. Kept tight: every rule here
// appeared in the live feed and none overlaps the From/To references.
// "causale movimento" is deliberately absent: it prefixes card rows (already
// excluded by their own markers) AND genuine SEPA credits to friends.
var p2pExcludes = []string{
	"carta n.", "circuito ", "cod. mcc", "c/o ", // card payments (Mediolanum format)
	"addebito diretto", "direct debit", "core rcur", "sdd", // direct debits
	"top-up", "top up", // self top-ups (Revolut/PayPal cards)
	"stipendio", "stipend", "salary", // payroll credits
	"recupero", "commissioni", "pagamenti paesi", "cashback", "refund", "rimborso", // fees/returns
	"exchange",                 // crypto conversions
	"cc pull",                  // card-account pulls
	"pos ", "atm ", "bancomat", // generic terminal markers
}

// sepaPersonMarkers: together with a SEPA order id ("Cod.Id.Ord:") and a
// person-shaped counterparty, these flag an inbound bank credit as
// person-to-person. Company payroll fails the person shape and refunds carry
// no marker, so neither leaks through.
var sepaPersonMarkers = []string{"versamento", "accredito"}

// companyTokens rule a counterparty name out of the "person-shaped" set
// (words are punctuation-stripped before the check: "s.r.l." -> "srl").
var companyTokens = map[string]bool{
	"srl": true, "srls": true, "spa": true, "sas": true, "snc": true,
	"scarl": true, "aps": true, "sarl": true, "ltd": true, "limited": true,
	"inc": true, "gmbh": true, "llc": true, "oy": true, "societa": true,
	"soc": true, "c": true,
}

// IsP2P reports whether a transaction is a transfer between people
// (conservative by design — see the tuning notes on P2PSignal).
func IsP2P(s P2PSignal) bool {
	if strings.TrimSpace(s.MerchantCategoryCode) != "" {
		return false // a card payment carries an MCC
	}
	merchant := NormalizeDescription(s.Merchant)
	hay := merchant + " " + strings.ToLower(s.Narrative)
	for _, m := range p2pExcludes {
		if strings.Contains(hay, m) {
			return false
		}
	}
	for _, seg := range narrativeSegments(s.Narrative) {
		if m := fromToRe.FindStringSubmatch(seg); m != nil {
			words := strings.Fields(m[1])
			if len(words) >= 2 || len([]rune(words[0])) >= 2 {
				return true
			}
		}
	}
	// Full SEPA credit ("... Cod.Id.Ord: ...") to a person-shaped counterparty.
	if personShaped(merchant) && strings.Contains(hay, "cod.id.ord") {
		for _, m := range sepaPersonMarkers {
			if strings.Contains(hay, m) {
				return true
			}
		}
	}
	return false
}

// ReferenceForRule returns the part of a P2P narration worth seeding a
// description rule from: the first segment that is not the From/To
// boilerplate ("spritz | From Riccardo C" -> "spritz"). Empty when the
// narration is only boilerplate ("From Riccardo C") — nothing to match on.
func ReferenceForRule(narrative string) string {
	for _, seg := range narrativeSegments(narrative) {
		if fromToRe.MatchString(seg) {
			continue
		}
		if s := NormalizeDescription(seg); s != "" {
			return s
		}
	}
	return ""
}

// narrativeSegments splits the bank's remittance narration (Enable Banking
// joins lines with " | ", but be liberal about newlines and commas).
func narrativeSegments(narrative string) []string {
	var out []string
	for _, seg := range strings.FieldsFunc(narrative, func(r rune) bool {
		return r == '|' || r == '\n' || r == ','
	}) {
		out = append(out, strings.TrimSpace(seg))
	}
	return out
}

// personShaped reports whether a counterparty name looks like a person:
// 2-4 words, no company tokens ("srl", "spa", "limited", ...). Initials and
// particles like "di" ("Di Florio Manuela") are fine.
func personShaped(name string) bool {
	var words []string
	for _, w := range strings.Fields(name) {
		cleaned := strings.Map(func(r rune) rune {
			if (r >= 'a' && r <= 'z') || (r >= 'à' && r <= 'ÿ') {
				return r
			}
			return -1
		}, w)
		if cleaned != "" {
			words = append(words, cleaned)
		}
	}
	if len(words) < 2 || len(words) > 4 {
		return false
	}
	for _, w := range words {
		if companyTokens[w] {
			return false
		}
	}
	return true
}
