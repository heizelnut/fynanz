package store

// BankLinkStatus values for bank_links.status.
const (
	LinkPendingConsent = "pending_consent"
	LinkActive         = "active"
	LinkExpired        = "expired"
	LinkRevoked        = "revoked"
	LinkError          = "error"
	LinkClosed         = "closed"
)

// BankLink is one Enable Banking consent session per user per bank.
type BankLink struct {
	ID              int64
	UserID          int64
	Bank            string
	Status          string
	AspspName       string
	AspspCountry    string
	AuthorizationID string
	SessionID       string
	State           string
	ValidUntil      string
	LastError       string
	LastSyncedAt    string
	CreatedAt       string
	UpdatedAt       string
}

// AccountInput carries the fields the sync layer extracts from the raw
// Enable Banking account resource. Kept in store (not sync) so store has no
// dependency on the upstream client types.
type AccountInput struct {
	UID                string
	IdentificationHash string
	Iban               string
	Name               string
	Details            string
	Product            string
	CashAccountType    string
	Usage              string
	Currency           string
	BalanceMinor       *int64
	BalanceCurrency    string
}

// Account is a stored account row.
type Account struct {
	AccountInput
	ID        int64
	UserID    int64
	LinkID    int64
	CreatedAt string
	UpdatedAt string
}

// TransactionInput is the extracted, normalized transaction payload.
type TransactionInput struct {
	EntryReference        string
	BookingDate           string
	ValueDate             string
	AmountMinor           int64
	Currency              string
	CreditDebit           string
	Status                string
	MerchantCategoryCode  string
	Counterparty          string
	CounterpartyAccount   string
	RemittanceInformation string
	Note                  string
	BalanceAfterMinor     *int64
	Raw                   string
}

// Transaction is a stored transaction row.
type Transaction struct {
	TransactionInput
	ID        int64
	UserID    int64
	AccountID int64
	CreatedAt string
}

// Category is a user-scoped category row.
type Category struct {
	ID       int64
	UserID   int64
	Name     string
	IsSystem bool
}
