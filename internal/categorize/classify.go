package categorize

import "strings"

// Rule is a merchant-pattern -> category mapping (mirrors the rules table;
// kept here so classify is a pure function).
type Rule struct {
	ID         int64
	CategoryID int64
	Pattern    string
	MatchType  string // "exact" (v1); "contains" later
}

// Classify returns the category for a normalized merchant, or nil / false if
// no rule matches. Exact match on normalized pattern (AGENTS.md §5 step 2);
// substring/fuzzy matching is deferred.
func Classify(rules []Rule, merchantNormalized string) (categoryID int64, found bool) {
	if merchantNormalized == "" {
		return 0, false
	}
	for _, r := range rules {
		switch strings.ToLower(r.MatchType) {
		case "", "exact":
			if NormalizeMerchant(r.Pattern) == merchantNormalized {
				return r.CategoryID, true
			}
		case "contains":
			if strings.Contains(merchantNormalized, NormalizeMerchant(r.Pattern)) {
				return r.CategoryID, true
			}
		}
	}
	return 0, false
}

// MerchantForRule returns the normalized pattern an answer should be stored
// as. Empty merchants cannot seed rules and are left to manual resolution.
func MerchantForRule(txRaw string) string {
	return NormalizeMerchant(txRaw)
}
