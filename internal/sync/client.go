// Package sync provides the Enable Banking API client (JWT-authenticated) and
// the sync runner that pulls accounts and transactions into the store.
package sync

import (
	"bytes"
	"context"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Sandbox and production share this single API host; which environment your
// requests run in is decided by the JWT's kid (the app's registration), not
// by the URL. Verify an app with `make creds`.
const ApiBaseURL = "https://api.enablebanking.com"

// APIError is a non-2xx Enable Banking response. Field tags follow the spec
// envelope: code is the int HTTP code, error is the text code (e.g.
// EXPIRED_SESSION).
type APIError struct {
	HTTPStatus int    `json:"code"`
	Message    string `json:"message"`
	Code       string `json:"error"`
	Detail     string `json:"detail"`
}

func (e *APIError) Error() string {
	s := fmt.Sprintf("enablebanking: %s (http %d)", e.Message, e.HTTPStatus)
	if e.Code != "" {
		s += fmt.Sprintf(" [%s]", e.Code)
	}
	if e.Detail != "" {
		s += ": " + e.Detail
	}
	return s
}

// Error codes signalling a dead consent that needs re-authorization.
var sessionErrorCodes = map[string]bool{
	"EXPIRED_SESSION":        true,
	"REVOKED_SESSION":        true,
	"CLOSED_SESSION":         true,
	"SESSION_DOES_NOT_EXIST": true,
}

// IsSessionError reports whether err means the bank link's session is no
// longer usable and the user must re-authorize.
func IsSessionError(err error) bool {
	var ae *APIError
	if !errors.As(err, &ae) {
		return false
	}
	return sessionErrorCodes[ae.Code]
}

// Error codes meaning "try again later" — the consent is fine, the bank or
// aggregator is throttled/timing out. These must not fire failure
// notifications nor damage the link state.
var retryableErrorCodes = map[string]bool{
	"ASPSP_RATE_LIMIT_EXCEEDED": true,
	"ASPSP_TIMEOUT":             true,
}

// IsRetryableError reports whether err is a transient throttle/timeout the
// next scheduled sync will likely survive.
func IsRetryableError(err error) bool {
	var ae *APIError
	if !errors.As(err, &ae) {
		return false
	}
	return retryableErrorCodes[ae.Code]
}

// Credentials is the identity a Client signs requests with. Configurable
// per request so credentials can live in the DB (Settings page) and change
// without restarting (see NewDynamicClient).
type Credentials struct {
	BaseURL    string
	AppID      string
	PrivateKey *rsa.PrivateKey
}

// CredentialResolver supplies the current credentials; errors are surfaced
// as API failures.
type CredentialResolver func() (Credentials, error)

// Client talks to the Enable Banking API. Each request carries a fresh JWT;
// no token caching needed since TTL is short.
// Client talks to the Enable Banking API. Each request carries a fresh JWT
// (no token caching; TTL is short). Credentials come from resolve, so the
// app can switch sandbox/production from the Settings page without restart.
type Client struct {
	resolve CredentialResolver
	http    *http.Client
	now     func() time.Time // clock for tests
}

// NewClient pins fixed credentials (tests, CLI tools).
func NewClient(baseURL, appID string, privateKey *rsa.PrivateKey) *Client {
	return NewDynamicClient(func() (Credentials, error) {
		return Credentials{BaseURL: baseURL, AppID: appID, PrivateKey: privateKey}, nil
	})
}

// NewDynamicClient resolves credentials on every call — the server uses this
// so Enable Banking settings edited in the UI apply immediately.
func NewDynamicClient(resolve CredentialResolver) *Client {
	return &Client{
		resolve: resolve,
		http:    &http.Client{Timeout: 30 * time.Second},
		now:     time.Now,
	}
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) (*http.Response, error) {
	cr, err := c.resolve()
	if err != nil {
		return nil, err
	}
	token, err := signClientJWT(cr.AppID, cr.PrivateKey, c.now())
	if err != nil {
		return nil, err
	}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(cr.BaseURL, "/")+path, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("enablebanking: %s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var ae APIError
		ae.HTTPStatus = resp.StatusCode
		// Try the spec'd envelope; fall back to raw body.
		if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&ae); err != nil {
			raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
			ae.Message = strings.TrimSpace(string(raw))
		}
		return resp, &ae
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return resp, fmt.Errorf("enablebanking: decode %s %s: %w", method, path, err)
		}
	}
	return resp, nil
}

// StartAuthorization begins the three-legged consent flow; the returned URL
// is where the PSU authorizes sharing with the app.
func (c *Client) StartAuthorization(ctx context.Context, req StartAuthorizationRequest) (*StartAuthorizationResponse, error) {
	var out StartAuthorizationResponse
	if _, err := c.do(ctx, http.MethodPost, "/auth", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// AuthorizeSession exchanges the callback code for a session and its accounts.
func (c *Client) AuthorizeSession(ctx context.Context, code string) (*AuthorizeSessionResponse, error) {
	var out AuthorizeSessionResponse
	if _, err := c.do(ctx, http.MethodPost, "/sessions", AuthorizeSessionRequest{Code: code}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetSession returns the session's status and account uids. Used by the sync
// job to detect expiry/revocation before fetching data.
func (c *Client) GetSession(ctx context.Context, sessionID string) (*GetSessionResponse, error) {
	var out GetSessionResponse
	if _, err := c.do(ctx, http.MethodGet, "/sessions/"+url.PathEscape(sessionID), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetAccountDetails fetches the full account resource for a session uid.
func (c *Client) GetAccountDetails(ctx context.Context, uid string) (*Account, error) {
	var out Account
	if _, err := c.do(ctx, http.MethodGet, "/accounts/"+url.PathEscape(uid)+"/details", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetBalances fetches the account's balances (first entry is the current
// booked/available balance, depending on what the bank returns).
func (c *Client) GetBalances(ctx context.Context, uid string) ([]BalanceResource, error) {
	var out BalancesResponse
	if _, err := c.do(ctx, http.MethodGet, "/accounts/"+url.PathEscape(uid)+"/balances", nil, &out); err != nil {
		return nil, err
	}
	return out.Balances, nil
}

// GetTransactions fetches all transactions from dateFrom (inclusive) using
// the continuation_key paging.
func (c *Client) GetTransactions(ctx context.Context, uid string, dateFrom, dateTo string) ([]Transaction, error) {
	var all []Transaction
	next := ""
	for {
		path := "/accounts/" + url.PathEscape(uid) + "/transactions"
		q := url.Values{}
		if dateFrom != "" {
			q.Set("date_from", dateFrom)
		}
		if dateTo != "" {
			q.Set("date_to", dateTo)
		}
		if next != "" {
			q.Set("continuation_key", next)
		}
		if enc := q.Encode(); enc != "" {
			path += "?" + enc
		}
		var page TransactionsResponse
		if _, err := c.do(ctx, http.MethodGet, path, nil, &page); err != nil {
			return nil, err
		}
		all = append(all, page.Transactions...)
		if page.ContinuationKey == nil || *page.ContinuationKey == "" {
			return all, nil
		}
		next = *page.ContinuationKey
	}
}

// GetApplication returns the app the current JWT authenticated as. Useful
// to verify credentials before starting the server.
func (c *Client) GetApplication(ctx context.Context) (*GetApplicationResponse, error) {
	var out GetApplicationResponse
	if _, err := c.do(ctx, http.MethodGet, "/application", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteSession revokes a consent session at the bank/aggregator side
// (DELETE /sessions/{id}), used when removing a bank link.
func (c *Client) DeleteSession(ctx context.Context, sessionID string) error {
	if _, err := c.do(ctx, http.MethodDelete, "/sessions/"+url.PathEscape(sessionID), nil, nil); err != nil {
		return err
	}
	return nil
}

// ListASPSPs returns banks available for the given country (used to resolve
// ASPSP names and consent validity limits).
func (c *Client) ListASPSPs(ctx context.Context, country string) ([]ASPSPData, error) {
	var out GetASPSPsResponse
	path := "/aspsps"
	if country != "" {
		path += "?country=" + url.QueryEscape(country)
	}
	if _, err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return out.ASPSPs, nil
}
