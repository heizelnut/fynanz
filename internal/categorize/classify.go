package categorize

import "strings"

// Field identifies which transaction attribute a rule matches against.
const (
	FieldMerchant    = "merchant"
	FieldDescription = "description"
)

// MatchType values for rules (match_type column).
const (
	MatchExact    = "exact"
	MatchContains = "contains"
	MatchFuzzy    = "fuzzy"
)

// Rule is a (field, pattern) -> category mapping with match semantics
// (mirrors the rules table; kept here so classify is a pure function).
type Rule struct {
	ID         int64
	CategoryID int64
	Pattern    string // normalized at insert time by the caller
	MatchType  string // MatchExact (default) | MatchContains | MatchFuzzy
	Field      string // FieldMerchant | FieldDescription
}

// Classify returns the category of the first rule that matches a
// transaction's text, or (0, false). Rules are evaluated in list order
// (store lists by id), so the earliest-learned answer wins on ties.
// merchant/description may be raw or pre-normalized — Matches normalizes
// internally and normalization is idempotent.
func Classify(rules []Rule, merchant, description string) (categoryID int64, found bool) {
	for _, r := range rules {
		if Matches(r, merchant, description) {
			return r.CategoryID, true
		}
	}
	return 0, false
}

// Matches reports whether one rule fires on a transaction's text.
func Matches(r Rule, merchant, description string) bool {
	var norm func(string) string
	target := merchant
	if r.Field == FieldDescription {
		target = description
		norm = NormalizeDescription
	} else {
		norm = NormalizeMerchant
	}
	pattern := norm(r.Pattern)
	if pattern == "" {
		return false
	}
	target = norm(target)
	switch strings.ToLower(r.MatchType) {
	case MatchFuzzy:
		return FuzzyMatch(pattern, target)
	case MatchContains:
		return strings.Contains(target, pattern)
	default: // "" and MatchExact
		return target == pattern
	}
}

// MerchantForRule returns the normalized pattern an answer should be stored
// as. Empty merchants cannot seed rules and are left to manual resolution.
func MerchantForRule(txRaw string) string {
	return NormalizeMerchant(txRaw)
}
