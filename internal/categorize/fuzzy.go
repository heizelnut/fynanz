package categorize

import (
	"strings"
	"unicode"
)

// Fuzzy word matching (AGENTS.md §5 "fuzzy/substring later" — landed).
//
// Semantics: a pattern wins when every significant word of it appears in the
// target — identical, or within one Levenshtein edit for words that are at
// least fuzzyEditLen characters (catches typos and plural/tense drift).
//
//   - AND over words: an unexpected target word never invalidates a match
//     ("amazon" still matches "amazon prime video").
//   - Order is irrelevant: "centrale mercato" matches "mercato centrale".
//   - Words shorter than minRuleWord are noise (initials, stopwords) and are
//     skipped, so "s market" needs only "market" to be present.
//   - Shorter words match exactly only: a 3-letter pattern like "pay" can
//     never fuzzy-match "day".

const (
	minRuleWord  = 3
	fuzzyEditLen = 4
)

// FuzzyMatch reports whether pattern textually matches target per the rules
// above. Both strings are normalized internally (idempotent — the caller may
// pass them already normalized).
func FuzzyMatch(pattern, target string) bool {
	pw := words(NormalizeDescription(pattern))
	tw := words(NormalizeDescription(target))
	if len(pw) == 0 || len(tw) == 0 {
		return false
	}
	any := false
	for _, p := range pw {
		if len([]rune(p)) < minRuleWord {
			continue
		}
		any = true
		if !wordIn(p, tw) {
			return false
		}
	}
	return any
}

// wordIn reports whether target words contain p exactly or within one edit
// (only for sufficiently long pattern words).
func wordIn(p string, tw []string) bool {
	pr := []rune(p)
	for _, t := range tw {
		if p == t {
			return true
		}
		if len(pr) >= fuzzyEditLen && editDistance(p, t) <= 1 {
			return true
		}
	}
	return false
}

// words splits text into runs of letters/digits (accents survive; punctuation
// and currency symbols act as separators).
func words(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

// editDistance is the Levenshtein distance (insertion/deletion/substitution).
// Words here are short, so the two-row DP is plenty.
func editDistance(a, b string) int {
	ar, br := []rune(a), []rune(b)
	prev := make([]int, len(br)+1)
	cur := make([]int, len(br)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ar); i++ {
		cur[0] = i
		for j := 1; j <= len(br); j++ {
			cost := 1
			if ar[i-1] == br[j-1] {
				cost = 0
			}
			cur[j] = min3(cur[j-1]+1, prev[j]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(br)]
}

func min3(a, b, c int) int {
	if a < b {
		if a < c {
			return a
		}
		return c
	}
	if b < c {
		return b
	}
	return c
}
