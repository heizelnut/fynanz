// Package sync provides the Enable Banking API client (JWT-authenticated) and
// the sync runner that pulls accounts and transactions into the store.
package sync

import (
	"bytes"
	"context"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultClient returns a client for the shared sandbox. Production apps use
// https://api.enablebanking.com (see .env.example).
const (
	SandboxBaseURL = "https://tilisy-sandbox.enablebanking.com"
	ProdBaseURL    = "https://api.enablebanking.com"
)

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
	ae, ok := err.(*APIError)
	if !ok {
		return false
	}
	return sessionErrorCodes[ae.Code]
}

// Client talks to the Enable Banking API. Each request carries a fresh JWT;
// no token caching needed since TTL is short.
type Client struct {
	baseURL    string
	appID      string
	privateKey *rsa.PrivateKey
	http       *http.Client
	now        func() time.Time // clock for tests
}

func NewClient(baseURL, appID string, privateKey *rsa.PrivateKey) *Client {
	return &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		appID:      appID,
		privateKey: privateKey,
		http:       &http.Client{Timeout: 30 * time.Second},
		now:        time.Now,
	}
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) (*http.Response, error) {
	token, err := signClientJWT(c.appID, c.privateKey, c.now())
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
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, rd)
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
