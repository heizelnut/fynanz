package sync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"fynanz/internal/metrics"
	"fynanz/internal/store"
)

// Service orchestrates the consent flow and the periodic sync job.
type Service struct {
	store          *store.Store
	client         *Client
	rec            metrics.Recorder
	redirectURL    string
	psuType        string
	historyDays    int
	maxConsentDays int
}

func NewService(st *store.Store, client *Client, rec metrics.Recorder, redirectURL, psuType string, historyDays, maxConsentDays int) *Service {
	return &Service{
		store:          st,
		client:         client,
		rec:            rec,
		redirectURL:    redirectURL,
		psuType:        psuType,
		historyDays:    historyDays,
		maxConsentDays: maxConsentDays,
	}
}

// ActiveLinks returns the user's currently authorized links (scheduler / UI).
func (s *Service) ActiveLinks(ctx context.Context) ([]store.BankLink, error) {
	userID, err := s.store.EnsureDefaultUser(ctx)
	if err != nil {
		return nil, err
	}
	return s.store.ActiveLinks(ctx, userID)
}

// aspspFor returns the ASPSP (name, country) defaults for a link's bank key
// (v1: sandbox/revolut/mediolanum; any other key is passed through).
func aspspFor(bank string) (name, country string) {
	switch bank {
	case "sandbox":
		return "Mock ASPSP", "FI"
	case "revolut":
		return "Revolut", "LT"
	case "mediolanum":
		return "Banca Mediolanum", "IT"
	default:
		return bank, "FI"
	}
}

// StartLink creates a pending bank link and starts the consent flow, returning
// the link row and the PSU consent URL.
func (s *Service) StartLink(ctx context.Context, bank string) (*store.BankLink, string, error) {
	userID, err := s.store.EnsureDefaultUser(ctx)
	if err != nil {
		return nil, "", err
	}
	name, country := aspspFor(bank)
	link, err := s.store.CreateLink(ctx, userID, bank, name, country)
	if err != nil {
		return nil, "", err
	}

	validUntil := time.Now().UTC().Add(time.Duration(s.maxConsentDays) * 24 * time.Hour).
		Format("2006-01-02T15:04:05.000000-07:00")
	req := StartAuthorizationRequest{
		Access:      Access{ValidUntil: validUntil},
		Aspsp:       ASPSP{Name: name, Country: country},
		State:       link.State,
		RedirectURL: s.redirectURL,
		PSUType:     s.psuType,
	}
	resp, err := s.client.StartAuthorization(ctx, req)
	if err != nil {
		_ = s.store.SetLinkStatus(ctx, link.ID, store.LinkError, err.Error())
		return nil, "", fmt.Errorf("sync: start auth for %s: %w", bank, err)
	}
	if err := s.store.SetLinkAuthorizing(ctx, link.ID, resp.AuthorizationID); err != nil {
		return nil, "", err
	}
	return link, resp.URL, nil
}

// Reconnect restarts the three-legged consent for an existing link row,
// reusing it (fresh state) instead of closing+recreating: bank_links has
// UNIQUE(user_id, bank), and accounts hang off the link.
func (s *Service) Reconnect(ctx context.Context, linkID int64) (string, error) {
	userID, err := s.store.EnsureDefaultUser(ctx)
	if err != nil {
		return "", err
	}
	link, err := s.store.GetLink(ctx, linkID, userID)
	if err != nil {
		return "", err
	}
	if link == nil {
		return "", fmt.Errorf("sync: no link %d", linkID)
	}
	state := uuid.NewString()
	if err := s.store.ResetLinkPending(ctx, link.ID, state); err != nil {
		return "", err
	}

	validUntil := time.Now().UTC().Add(time.Duration(s.maxConsentDays) * 24 * time.Hour).
		Format("2006-01-02T15:04:05.000000-07:00")
	resp, err := s.client.StartAuthorization(ctx, StartAuthorizationRequest{
		Access:      Access{ValidUntil: validUntil},
		Aspsp:       ASPSP{Name: link.AspspName, Country: link.AspspCountry},
		State:       state,
		RedirectURL: s.redirectURL,
		PSUType:     s.psuType,
	})
	if err != nil {
		_ = s.store.SetLinkStatus(ctx, link.ID, store.LinkError, err.Error())
		return "", fmt.Errorf("sync: reauth %s: %w", link.Bank, err)
	}
	if err := s.store.SetLinkAuthorizing(ctx, link.ID, resp.AuthorizationID); err != nil {
		return "", err
	}
	return resp.URL, nil
}

// CompleteLink finishes the consent callback (code+state), activates the
// session and runs the first sync.
func (s *Service) CompleteLink(ctx context.Context, state, code string) error {
	link, err := s.store.GetLinkByState(ctx, state)
	if err != nil {
		return err
	}
	if link == nil {
		return errors.New("sync: unknown state in callback")
	}
	if link.Status != store.LinkPendingConsent {
		return fmt.Errorf("sync: link %d already %s", link.ID, link.Status)
	}

	auth, err := s.client.AuthorizeSession(ctx, code)
	if err != nil {
		return fmt.Errorf("sync: authorize: %w", err)
	}
	if err := s.store.ActivateLink(ctx, link.ID, auth.SessionID, auth.Access.ValidUntil); err != nil {
		return err
	}
	_, err = s.SyncLink(ctx, link.ID)
	return err
}

// Result summarizes one sync run for a link.
type Result struct {
	Link                 store.BankLink
	Accounts             int
	TransactionsInserted int64
	TransactionsSkipped  int64
	MarkedReauthNeeded   bool
}

// SyncLink pulls accounts, balances and transactions for one active link.
func (s *Service) SyncLink(ctx context.Context, linkID int64) (res Result, err error) {
	// Record Prometheus outcomes on every exit path (AGENTS.md §8).
	defer func() {
		if err != nil {
			if res.MarkedReauthNeeded || IsSessionError(err) {
				s.rec.ReauthNeeded(res.Link.Bank)
			} else {
				s.rec.RunFailed(res.Link.Bank)
			}
			return
		}
		s.rec.RunSucceeded(res.Link.Bank)
		s.rec.Ingested(res.Link.Bank, res.TransactionsInserted)
		s.rec.SetAccounts(res.Link.Bank, res.Accounts)
	}()

	userID, err := s.store.EnsureDefaultUser(ctx)
	if err != nil {
		return Result{}, err
	}
	link, err := s.store.GetLink(ctx, linkID, userID)
	if err != nil {
		return Result{}, err
	}
	if link == nil {
		return Result{}, fmt.Errorf("sync: no link %d", linkID)
	}
	if link.Status != store.LinkActive || link.SessionID == "" {
		return Result{}, fmt.Errorf("sync: link %q not authorized", link.Bank)
	}

	res = Result{Link: *link}

	ses, err := s.client.GetSession(ctx, link.SessionID)
	if err != nil {
		if IsSessionError(err) {
			s.markReauth(ctx, link.ID, err)
			res.MarkedReauthNeeded = true
		}
		return res, err
	}
	if ses.Status != SessionAuthorized {
		status, msg := sessionStatus(link.Bank, ses.Status)
		_ = s.store.SetLinkStatus(ctx, link.ID, status, msg)
		res.MarkedReauthNeeded = status != store.LinkActive
		return res, fmt.Errorf("sync: session %s for %s: %s", link.Bank, link.SessionID, ses.Status)
	}

	dateFrom := time.Now().UTC().AddDate(0, 0, -s.historyDays).Format("2006-01-02")

	for _, sa := range ses.AccountsData {
		details, err := s.client.GetAccountDetails(ctx, sa.UID)
		if err != nil {
			return res, fmt.Errorf("sync: account details %s: %w", sa.UID, err)
		}
		in := store.AccountInput{
			UID:                sa.UID,
			IdentificationHash: sa.IdentificationHash,
			Iban:               details.AccountID.Iban,
			Name:               details.Name,
			Details:            details.Details,
			Product:            details.Product,
			CashAccountType:    details.CashAccountType,
			Usage:              details.Usage,
			Currency:           details.Currency,
		}
		if bal, balErr := s.client.GetBalances(ctx, sa.UID); balErr == nil && len(bal) > 0 {
			minor, convErr := amountToMinor(bal[0].BalanceAmount.Amount, bal[0].BalanceAmount.Currency)
			if convErr == nil {
				in.BalanceMinor = &minor
				in.BalanceCurrency = bal[0].BalanceAmount.Currency
			}
		}
		accountIDs, err := s.store.UpsertAccounts(ctx, userID, link.ID, []store.AccountInput{in})
		if err != nil {
			return res, err
		}
		accountID, ok := accountIDs[sa.UID]
		if !ok {
			return res, fmt.Errorf("sync: missing account id for %s", sa.UID)
		}

		rawTxns, err := s.client.GetTransactions(ctx, sa.UID, dateFrom, "")
		if err != nil {
			if IsSessionError(err) {
				s.markReauth(ctx, link.ID, err)
				res.MarkedReauthNeeded = true
			}
			return res, fmt.Errorf("sync: transactions %s: %w", sa.UID, err)
		}
		res.Accounts++

		inputs := make([]store.TransactionInput, 0, len(rawTxns))
		for _, t := range rawTxns {
			inp, err := toTransactionInput(t)
			if err != nil {
				return res, fmt.Errorf("sync: convert transaction: %w", err)
			}
			inputs = append(inputs, inp)
		}
		inserted, skipped, err := s.store.InsertTransactions(ctx, userID, accountID, inputs)
		if err != nil {
			return res, err
		}
		res.TransactionsInserted += inserted
		res.TransactionsSkipped += skipped
	}

	if err := s.store.TouchLinkSync(ctx, link.ID); err != nil {
		return res, err
	}
	return res, nil
}

// SyncAll runs SyncLink for every active link. Errors are returned per link
// via a map (a failed sync must not abort the others).
func (s *Service) SyncAll(ctx context.Context) map[int64]error {
	userID, err := s.store.EnsureDefaultUser(ctx)
	if err != nil {
		return map[int64]error{0: err}
	}
	links, err := s.store.ActiveLinks(ctx, userID)
	if err != nil {
		return map[int64]error{0: err}
	}
	errs := make(map[int64]error, len(links))
	for _, l := range links {
		if _, err := s.SyncLink(ctx, l.ID); err != nil {
			errs[l.ID] = err
		}
	}
	return errs
}

func (s *Service) markReauth(ctx context.Context, linkID int64, cause error) {
	_ = s.store.SetLinkStatus(ctx, linkID, store.LinkExpired,
		fmt.Sprintf("session expired/revoked: %v", cause))
}

// sessionStatus maps a remote session status to a local link status + message.
func sessionStatus(bank, remote string) (string, string) {
	switch remote {
	case SessionExpired:
		return store.LinkExpired, "session expired at bank"
	case SessionRevoked, SessionCancelled, SessionInvalid:
		return store.LinkRevoked, "session " + remote
	case SessionClosed, SessionPendingAuth:
		return store.LinkError, "session " + remote
	default:
		return store.LinkError, "unexpected session status " + remote
	}
}

// toTransactionInput normalizes a raw API transaction into a store row.
func toTransactionInput(t Transaction) (store.TransactionInput, error) {
	minor, err := normalizeAmount(t.TransactionAmount, t.CreditDebitIndicator)
	if err != nil {
		return store.TransactionInput{}, err
	}
	in := store.TransactionInput{
		EntryReference:        t.EntryReference,
		BookingDate:           t.BookingDate,
		ValueDate:             t.ValueDate,
		AmountMinor:           minor,
		Currency:              t.TransactionAmount.Currency,
		CreditDebit:           t.CreditDebitIndicator,
		Status:                t.Status,
		MerchantCategoryCode:  t.MerchantCategoryCode,
		RemittanceInformation: joinStrings(t.RemittanceInformation, " | "),
		Note:                  t.Note,
	}
	// Counterparty: for expenses the creditor (we paid them), for income the
	// debtor (they paid us) — plus their account, when present.
	if t.CreditDebitIndicator == "DBIT" {
		if t.Creditor != nil {
			in.Counterparty = t.Creditor.Name
		}
		if t.CreditorAccount != nil {
			in.CounterpartyAccount = t.CreditorAccount.Iban
		}
	} else {
		if t.Debtor != nil {
			in.Counterparty = t.Debtor.Name
		}
		if t.DebtorAccount != nil {
			in.CounterpartyAccount = t.DebtorAccount.Iban
		}
	}
	if t.BankTransactionCode != nil {
		in.Note = joinStrings([]string{t.BankTransactionCode.Description, in.Note}, " | ")
	}
	if t.BalanceAfterTransaction != nil {
		b, convErr := amountToMinor(t.BalanceAfterTransaction.Amount, t.BalanceAfterTransaction.Currency)
		if convErr == nil {
			in.BalanceAfterMinor = &b
		}
	}
	if raw, err := json.Marshal(t); err == nil {
		in.Raw = string(raw)
	}
	return in, nil
}

func joinStrings(parts []string, sep string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += sep
		}
		out += p
	}
	return out
}
