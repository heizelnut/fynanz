package categorize

import (
	"strings"
)

// OwnAccount is one of the user's linked accounts, used to recognize internal
// transfers (AGENTS.md §4 / §5: not double-counted as income/expense).
type OwnAccount struct {
	Name string
	Iban string
}

func normIBAN(iban string) string {
	return strings.ToUpper(strings.Join(strings.Fields(iban), ""))
}

// IsInternalTransfer reports whether the counterparty of a transaction is one
// of the user's own accounts (by IBAN, or by account name as a fallback).
func IsInternalTransfer(counterpartyName, counterpartyAccount string, own []OwnAccount) bool {
	if len(own) == 0 {
		return false
	}
	acc := normIBAN(counterpartyAccount)
	name := strings.ToLower(strings.TrimSpace(counterpartyName))
	for _, o := range own {
		if acc != "" && normIBAN(o.Iban) == acc {
			return true
		}
		if name != "" && strings.ToLower(strings.TrimSpace(o.Name)) == name {
			return true
		}
	}
	return false
}
