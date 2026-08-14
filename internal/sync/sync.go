package sync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"fynanz/internal/categorize"
	"fynanz/internal/metrics"
	"fynanz/internal/notify"
	"fynanz/internal/store"
)

// Service orchestrates the consent flow and the periodic sync job.
type Service struct {
	store          *store.Store
	client         *Client
	rec            metrics.Recorder
	notifier       notify.Notifier
	baseURL        string
	redirectURL    string
	psuType        string
	historyDays    int
	maxConsentDays int
}

// NewService wires the sync layer. notifier may be nil (no notifications);
// baseURL is the public origin used for notification deep links.
func NewService(st *store.Store, client *Client, rec metrics.Recorder, notifier notify.Notifier, baseURL, redirectURL, psuType string, historyDays, maxConsentDays int) *Service {
	if notifier == nil {
		notifier = notify.Nop{}
	}
	return &Service{
		store:          st,
		client:         client,
		rec:            rec,
		notifier:       notifier,
		baseURL:        strings.TrimRight(baseURL, "/"),
		redirectURL:    redirectURL,
		psuType:        psuType,
		historyDays:    historyDays,
		maxConsentDays: maxConsentDays,
	}
}

// notifyEvent sends a payload without letting it fail the sync, and survives
// request cancellation (the scheduler uses a long-lived context anyway).
func (s *Service) notifyEvent(ctx context.Context, p notify.Payload) {
	_ = s.notifier.Notify(context.WithoutCancel(ctx), p)
}

// RemoveLink disconnects a bank. purge=false keeps all synced data (the row
// is marked removed; a later connect reattaches the same accounts);
// purge=true deletes the link, its accounts and their transactions. The
// PSD2 session is revoked remotely best-effort.
func (s *Service) RemoveLink(ctx context.Context, linkID int64, purge bool) error {
	userID, err := s.store.EnsureDefaultUser(ctx)
	if err != nil {
		return err
	}
	link, err := s.store.GetLink(ctx, linkID, userID)
	if err != nil {
		return err
	}
	if link == nil {
		return fmt.Errorf("sync: no link %d", linkID)
	}
	if purge {
		return s.store.PurgeLink(ctx, userID, linkID)
	}
	if link.SessionID != "" {
		// Remote revoke is best-effort: session may already have expired.
		_ = s.client.DeleteSession(ctx, link.SessionID)
	}
	s.rec.SetAccounts(link.Bank, 0)
	return s.store.MarkLinkRemoved(ctx, userID, linkID)
}

// ActiveLinks returns the user's currently authorized links (scheduler / UI).
func (s *Service) ActiveLinks(ctx context.Context) ([]store.BankLink, error) {
	userID, err := s.store.EnsureDefaultUser(ctx)
	if err != nil {
		return nil, err
	}
	return s.store.ActiveLinks(ctx, userID)
}

// resolveASPSP returns the authoritative (name, country, consent-cap) for a
// bank key from the live /aspsps list (names differ per environment — sandbox
// shows only Mock ASPSP, production shows real banks). Falls back to
// curated defaults when the list is unreachable or the bank isn't in it.
func (s *Service) resolveASPSP(ctx context.Context, bank string) (name, country string, maxConsentSecs *int64) {
	name, country = aspspFor(bank)
	list, err := s.client.ListASPSPs(ctx, country)
	if err != nil {
		return name, country, nil
	}
	needle := strings.ToLower(strings.TrimSpace(bank))
	for _, a := range list {
		if strings.ToLower(strings.TrimSpace(a.Name)) == needle ||
			strings.Contains(strings.ToLower(a.Name), needle) {
			return a.Name, a.Country, a.MaxConsentValidity
		}
	}
	return name, country, nil
}

// consentValidity caps the requested consent at the bank's own maximum
// (the API rejects values beyond now + maximum_consent_validity) and at the
// configured default (env or Settings-page override).
func (s *Service) consentValidity(ctx context.Context, userID int64, bankMaxSecs *int64) time.Duration {
	capD := time.Duration(s.consentDaysFor(ctx, userID)) * 24 * time.Hour
	if bankMaxSecs != nil && *bankMaxSecs > 0 {
		if bank := time.Duration(*bankMaxSecs) * time.Second; bank < capD {
			capD = bank
		}
	}
	return capD
}

// psuTypeFor and consentDaysFor honour Settings-page overrides on top of the
// env defaults (config resolved per connect, no restart needed).
func (s *Service) psuTypeFor(ctx context.Context, userID int64) string {
	if v, err := s.store.GetSetting(ctx, userID, store.SettingEBPSUType); err == nil && v != "" {
		return v
	}
	return s.psuType
}

func (s *Service) consentDaysFor(ctx context.Context, userID int64) int {
	if v, err := s.store.GetSetting(ctx, userID, store.SettingEBMaxConsentDays); err == nil && v != "" {
		if n, err2 := strconv.Atoi(v); err2 == nil && n > 0 {
			return n
		}
	}
	return s.maxConsentDays
}

// aspspFor returns the default (name, country) guesses for a link's bank key
// (used as fallback when the /aspsps list is unreachable).
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
	name, country, maxSecs := s.resolveASPSP(ctx, bank)
	link, err := s.store.CreateLink(ctx, userID, bank, name, country)
	if err != nil {
		return nil, "", err
	}

	validUntil := time.Now().UTC().Add(s.consentValidity(ctx, userID, maxSecs)).
		Format("2006-01-02T15:04:05.000000-07:00")
	req := StartAuthorizationRequest{
		Access:      Access{ValidUntil: validUntil},
		Aspsp:       ASPSP{Name: name, Country: country},
		State:       link.State,
		RedirectURL: s.redirectURL,
		PSUType:     s.psuTypeFor(ctx, userID),
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

	validUntil := time.Now().UTC().Add(s.consentValidity(ctx, userID, nil)).
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
	var userID int64 // declared before the defer, which records outcomes
	// Record Prometheus outcomes on every exit path (AGENTS.md §8) and fire
	// the matching notifications (§7): reauth/sync-failed on errors, the
	// "needs categorization" prompt after a successful run.
	defer func() {
		now := time.Now().UTC().Format(time.RFC3339)
		if err != nil {
			if res.MarkedReauthNeeded || IsSessionError(err) {
				s.rec.ReauthNeeded(res.Link.Bank)
				if res.Link.Bank == "" {
					return
				}
				s.notifyEvent(ctx, notify.Payload{
					Event:     notify.EventReauthNeeded,
					Title:     "Bank link needs re-authorization",
					Message:   fmt.Sprintf("The %s link is no longer authorized (PSD2 sessions expire). Re-authorize to keep syncing.", res.Link.Bank),
					Account:   res.Link.Bank,
					Timestamp: now,
					Link:      s.baseURL + "/links/" + strconv.FormatInt(linkID, 10) + "/reconnect",
				})
			} else {
				s.rec.RunFailed(res.Link.Bank)
				if res.Link.Bank == "" {
					return
				}
				s.notifyEvent(ctx, notify.Payload{
					Event:     notify.EventSyncFailed,
					Title:     "Sync failed",
					Message:   fmt.Sprintf("Syncing %s failed: %v", res.Link.Bank, err),
					Account:   res.Link.Bank,
					Timestamp: now,
					Link:      s.baseURL,
				})
			}
			return
		}
		s.rec.RunSucceeded(res.Link.Bank)
		s.rec.Ingested(res.Link.Bank, res.TransactionsInserted)
		s.rec.SetAccounts(res.Link.Bank, res.Accounts)
		s.notifyUncategorized(ctx, userID, res.Link.Bank, now, res.TransactionsInserted)
	}()

	userID, err = s.store.EnsureDefaultUser(ctx)
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

	// Categorization context (AGENTS.md §5): rules + own accounts for
	// transfer detection, loaded once per run.
	rules, err := s.store.ListRules(ctx, userID)
	if err != nil {
		return res, err
	}
	ownAccounts, err := s.store.ListAccounts(ctx, userID)
	if err != nil {
		return res, err
	}
	own := make([]categorize.OwnAccount, 0, len(ownAccounts))
	for _, a := range ownAccounts {
		own = append(own, categorize.OwnAccount{Name: a.Name, Iban: a.Iban})
	}
	transferCatID := s.transferCategoryID(ctx, userID)

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
			s.categorizeInput(&inp, toCatRules(rules), own, transferCatID)
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
	// Reclassify so transfers are caught even on the first sync, when the
	// account list was still empty at classification time. Idempotent:
	// scans only uncategorized rows and rewrites what it finds.
	_ = s.Reclassify(ctx)
	return res, nil
}

// toCatRules converts stored rules to the pure categorize package's shape.
func toCatRules(rules []store.Rule) []categorize.Rule {
	out := make([]categorize.Rule, 0, len(rules))
	for _, r := range rules {
		out = append(out, categorize.Rule{ID: r.ID, CategoryID: r.CategoryID, Pattern: r.Pattern, MatchType: r.MatchType})
	}
	return out
}

// categorizeInput assigns merchant_normalized, internal-transfer flag and a
// rule-based category to a transaction before it is stored.
func (s *Service) categorizeInput(inp *store.TransactionInput, rules []categorize.Rule, own []categorize.OwnAccount, transferCatID *int64) {
	src := strings.TrimSpace(inp.Counterparty)
	if src == "" && inp.RemittanceInformation != "" {
		// Some banks (e.g. sandbox mock income rows) omit debtor/creditor and
		// put the name into the remittance info.
		src = strings.SplitN(inp.RemittanceInformation, " | ", 2)[0]
	}
	inp.MerchantNormalized = categorize.NormalizeMerchant(src)

	if categorize.IsInternalTransfer(inp.Counterparty, inp.CounterpartyAccount, own) {
		inp.InternalTransfer = true
		if transferCatID != nil {
			id := *transferCatID
			inp.CategoryID = &id
			inp.CategorySource = store.CatSourceTransfer
		}
		return
	}
	if id, ok := categorize.Classify(rules, inp.MerchantNormalized); ok {
		inp.CategoryID = &id
		inp.CategorySource = store.CatSourceRule
	}
}

// transferCategoryID resolves the 'Transfers (internal)' category (may be
// renamed by the user), nil if absent.
func (s *Service) transferCategoryID(ctx context.Context, userID int64) *int64 {
	cats, err := s.store.ListCategories(ctx, userID)
	if err != nil {
		return nil
	}
	for _, c := range cats {
		if strings.EqualFold(strings.TrimSpace(c.Name), "transfers (internal)") {
			id := c.ID
			return &id
		}
	}
	return nil
}

// Reclassify re-derives merchant + category for every uncategorized row (run
// at startup so rules added later also apply to older transactions).
func (s *Service) Reclassify(ctx context.Context) error {
	userID, err := s.store.EnsureDefaultUser(ctx)
	if err != nil {
		return err
	}
	rules, err := s.store.ListRules(ctx, userID)
	if err != nil {
		return err
	}
	ownAccounts, err := s.store.ListAccounts(ctx, userID)
	if err != nil {
		return err
	}
	own := make([]categorize.OwnAccount, 0, len(ownAccounts))
	for _, a := range ownAccounts {
		own = append(own, categorize.OwnAccount{Name: a.Name, Iban: a.Iban})
	}
	transferCatID := s.transferCategoryID(ctx, userID)

	for {
		txs, err := s.store.ListUncategorized(ctx, userID, batchSize, 0)
		if err != nil {
			return err
		}
		if len(txs) == 0 {
			break
		}
		updates := make([]store.CategoryUpdate, 0, len(txs))
		for _, t := range txs {
			inp := t.TransactionInput
			s.categorizeInput(&inp, toCatRules(rules), own, transferCatID)
			updates = append(updates, store.CategoryUpdate{
				TxID:               t.ID,
				MerchantNormalized: inp.MerchantNormalized,
				CategoryID:         inp.CategoryID,
				Source:             inp.CategorySource,
				InternalTransfer:   inp.InternalTransfer,
			})
		}
		if err := s.store.ApplyCategoryUpdates(ctx, userID, updates); err != nil {
			return err
		}
		if len(txs) < batchSize {
			break
		}
	}
	s.updateUncategorizedGauge(ctx, userID)
	return nil
}

const batchSize = 500

// ResolveInbox categorizes one inbox item and grows the rules table from the
// user's answer (AGENTS.md §5: exact merchant match at minimum). Returns the
// rule pattern used (empty when there is nothing to match on).
func (s *Service) ResolveInbox(ctx context.Context, txID, categoryID int64) (string, error) {
	userID, err := s.store.EnsureDefaultUser(ctx)
	if err != nil {
		return "", err
	}
	tx, err := s.store.GetTransaction(ctx, userID, txID)
	if err != nil {
		return "", err
	}
	if err := s.store.SetTransactionCategory(ctx, userID, txID, &categoryID, store.CatSourceRule); err != nil {
		return "", err
	}
	pattern := tx.MerchantNormalized
	if pattern != "" {
		if err := s.store.InsertRule(ctx, userID, categoryID, pattern, "exact"); err != nil {
			return "", err
		}
	}
	s.updateUncategorizedGauge(ctx, userID)
	return pattern, nil
}

// OverrideCategory sets a category manually (no rule written) — used from the
// transactions list for corrections at any time.
func (s *Service) OverrideCategory(ctx context.Context, txID, categoryID int64) error {
	userID, err := s.store.EnsureDefaultUser(ctx)
	if err != nil {
		return err
	}
	err = s.store.SetTransactionCategory(ctx, userID, txID, &categoryID, store.CatSourceManual)
	if err == nil {
		s.updateUncategorizedGauge(ctx, userID)
	}
	return err
}

// notifyUncategorized sends the real-time prompt (§4/§7) when a sync landed
// new transactions and the inbox is not empty.
func (s *Service) notifyUncategorized(ctx context.Context, userID int64, bank, now string, inserted int64) {
	if inserted <= 0 {
		return
	}
	items, err := s.store.ListUncategorized(ctx, userID, 5, 0)
	if err != nil || len(items) == 0 {
		return
	}
	first := items[0]
	msg := fmt.Sprintf("%d transaction(s) from %s need a category", len(items), bank)
	if first.MerchantNormalized != "" {
		msg += fmt.Sprintf(" — e.g. %s", first.MerchantNormalized)
	}
	if first.Counterparty != "" {
		msg += fmt.Sprintf(" (%s)", first.Counterparty)
	}
	msg += fmt.Sprintf(", %s %s.", FormatMinor(first.AmountMinor, first.Currency), first.Currency)
	s.notifyEvent(ctx, notify.Payload{
		Event:       notify.EventNeedsCategory,
		Title:       "New transactions need a category",
		Message:     msg,
		AmountMinor: first.AmountMinor,
		Currency:    first.Currency,
		Merchant:    first.MerchantNormalized,
		Account:     bank,
		Timestamp:   now,
		Link:        s.baseURL + "/inbox",
	})
}

func (s *Service) updateUncategorizedGauge(ctx context.Context, userID int64) {
	if n, err := s.store.UncategorizedCount(ctx, userID); err == nil {
		s.rec.SetUncategorized(n)
	}
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
