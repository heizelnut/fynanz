// Package categorize is the pure-matching half of categorization: merchant
// normalization, rule matching and internal-transfer detection. Persistence
// and the resolve flow live in internal/store + internal/sync (AGENTS.md §5).
package categorize

import (
	"regexp"
	"strings"
)

// artifactRe strips sandbox/mock artifacts appended to merchant strings, e.g.
// "Ella Hämäläinen-CRDT-6.94-f0ao3" -> "Ella Hämäläinen". The shape
// (<CRDT|DBIT>-<amount>-<random>) is used by Enable Banking's Mock ASPSP;
// real banks use proper creditor/debtor fields, so this is a safe cleanup
// that cannot corrupt legitimate merchant names.
var artifactRe = regexp.MustCompile(`(?i)\s*-?\s*(crdt|dbit)\s*-\s*[0-9]+(\.[0-9]+)?\s*-\s*[a-z0-9]+\s*$`)

// NormalizeMerchant turns a raw counterparty/remittance string into the
// canonical form used for rule matching and storage: lowercase, single
// spaces, artifact tail removed.
func NormalizeMerchant(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	s = strings.ToLower(s)
	s = artifactRe.ReplaceAllString(s, "")
	s = strings.Join(strings.Fields(s), " ")
	return s
}
