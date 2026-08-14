package sync

// Go types mirroring the Enable Banking OpenAPI spec (v1.0.0-ef13dc17,
// https://enablebanking.com/docs/api/reference/). Field names follow the spec;
// JSON tags are lowercase like the wire format.

// AmountType: monetary value as a decimal *string* (precision varies by
// currency), signed per credit/debit flow.
type Amount struct {
	Currency string `json:"currency"`
	Amount   string `json:"amount"`
}

// Access is the consent scope requested from the ASPSP.
type Access struct {
	ValidUntil   string `json:"valid_until"`
	Transactions *bool  `json:"transactions,omitempty"`
	Balances     *bool  `json:"balances,omitempty"`
}

// ASPSP identifies a bank by (name, country) pair.
type ASPSP struct {
	Name    string `json:"name"`
	Country string `json:"country"`
}

type StartAuthorizationRequest struct {
	Access      Access `json:"access"`
	Aspsp       ASPSP  `json:"aspsp"`
	State       string `json:"state"`
	RedirectURL string `json:"redirect_url"`
	PSUType     string `json:"psu_type"`
}

type StartAuthorizationResponse struct {
	URL             string `json:"url"`
	AuthorizationID string `json:"authorization_id"`
	PSUIDHash       string `json:"psu_id_hash"`
}

type AuthorizeSessionRequest struct {
	Code string `json:"code"`
}

type AuthorizeSessionResponse struct {
	SessionID string    `json:"session_id"`
	Accounts  []Account `json:"accounts"`
	Aspsp     ASPSP     `json:"aspsp"`
	PSUType   string    `json:"psu_type"`
	Access    Access    `json:"access"`
}

// Account is an AccountResource: account metadata plus the session-scoped uid
// used to fetch balances/transactions.
type Account struct {
	AccountID            AccountIdentification `json:"account_id"`
	Name                 string                `json:"name"`
	Details              string                `json:"details"`
	Usage                string                `json:"usage"`
	CashAccountType      string                `json:"cash_account_type"`
	Product              string                `json:"product"`
	Currency             string                `json:"currency"`
	UID                  string                `json:"uid"`
	IdentificationHash   string                `json:"identification_hash"`
	IdentificationHashes []string              `json:"identification_hashes"`
}

type AccountIdentification struct {
	Iban  string                 `json:"iban"`
	Other *GenericIdentification `json:"other"`
}

type GenericIdentification struct {
	Identification string `json:"identification"`
	SchemeName     string `json:"scheme_name"`
}

// Session statuses (SessionStatus enum).
const (
	SessionInvalid          = "INVALID"
	SessionPendingAuth      = "PENDING_AUTHORIZATION"
	SessionReturnedFromBank = "RETURNED_FROM_BANK"
	SessionAuthorized       = "AUTHORIZED"
	SessionExpired          = "EXPIRED"
	SessionClosed           = "CLOSED"
	SessionRevoked          = "REVOKED"
	SessionCancelled        = "CANCELLED"
)

type SessionAccount struct {
	UID                  string   `json:"uid"`
	IdentificationHash   string   `json:"identification_hash"`
	IdentificationHashes []string `json:"identification_hashes"`
}

type GetSessionResponse struct {
	Status       string           `json:"status"`
	Accounts     []string         `json:"accounts"`
	AccountsData []SessionAccount `json:"accounts_data"`
	Aspsp        ASPSP            `json:"aspsp"`
	PSUType      string           `json:"psu_type"`
	Access       Access           `json:"access"`
	Created      string           `json:"created"`
	Authorized   string           `json:"authorized"`
}

type BalanceResource struct {
	Name          string `json:"name"`
	BalanceAmount Amount `json:"balance_amount"`
	BalanceType   string `json:"balance_type"`
	ReferenceDate string `json:"reference_date"`
}

type BalancesResponse struct {
	Balances []BalanceResource `json:"balances"`
}

// Transaction shadows the OpenAPI Transaction schema; fields we don't need
// yet (exchange_rate, agent details, ...) are dropped.
type Transaction struct {
	EntryReference          string                 `json:"entry_reference"`
	MerchantCategoryCode    string                 `json:"merchant_category_code"`
	TransactionAmount       Amount                 `json:"transaction_amount"`
	Creditor                *PartyIdentification   `json:"creditor"`
	CreditorAccount         *AccountIdentification `json:"creditor_account"`
	Debtor                  *PartyIdentification   `json:"debtor"`
	DebtorAccount           *AccountIdentification `json:"debtor_account"`
	BankTransactionCode     *BankTransactionCode   `json:"bank_transaction_code"`
	CreditDebitIndicator    string                 `json:"credit_debit_indicator"`
	Status                  string                 `json:"status"`
	BookingDate             string                 `json:"booking_date"`
	ValueDate               string                 `json:"value_date"`
	BalanceAfterTransaction *Amount                `json:"balance_after_transaction"`
	RemittanceInformation   []string               `json:"remittance_information"`
	Note                    string                 `json:"note"`
}

type PartyIdentification struct {
	Name  string            `json:"name"`
	Other map[string]string `json:"other,omitempty"`
}

type BankTransactionCode struct {
	Code        string `json:"code"`
	Description string `json:"description"`
}

type TransactionsResponse struct {
	Transactions    []Transaction `json:"transactions"`
	ContinuationKey *string       `json:"continuation_key"`
}

type GetASPSPsResponse struct {
	ASPSPs []ASPSPData `json:"aspsps"`
}

type ASPSPData struct {
	Name               string       `json:"name"`
	Country            string       `json:"country"`
	PSUTypes           []string     `json:"psu_types,omitempty"`
	Sandbox            *SandboxInfo `json:"sandbox,omitempty"`
	MaxConsentValidity *int64       `json:"maximum_consent_validity,omitempty"`
}

type SandboxInfo struct {
	Users []SandboxUser `json:"users"`
}

type SandboxUser struct {
	Username string `json:"username"`
	Password string `json:"password"`
	OTP      string `json:"otp"`
}

// GetApplicationResponse: identifies the app the JWT authenticated as
// (GET /application) — handy for validating credentials.
type GetApplicationResponse struct {
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	Kid          string   `json:"kid"`
	Environment  string   `json:"environment"`
	RedirectURLs []string `json:"redirect_urls"`
	Active       bool     `json:"active"`
	Countries    []string `json:"countries"`
	Services     []string `json:"services"`
}

// ErrorResponse is the API error envelope (spec: message required).
type ErrorResponse struct {
	Message string `json:"message"`
	Code    int    `json:"code"`
	Error   string `json:"error"`
	Detail  string `json:"detail"`
}
