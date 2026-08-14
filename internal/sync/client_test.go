package sync

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// fakeEB is an in-memory Enable Banking API for tests. It validates that
// requests carry a JWT bearer token and serves canned responses per path.
type fakeEB struct {
	t           *testing.T
	authURL     string
	sessionResp AuthorizeSessionResponse
	txnsPage1   TransactionsResponse
	txnsPage2   TransactionsResponse
	details     Account
	balances    BalancesResponse
	statusCode  int // override for error-path tests
	errBody     string
	mu          sync.Mutex
	sessionErr  *APIError     // when set, GET /sessions/{id} fails with it
	txnCalls    int           // transaction fetches so far
	txnsAfter   []Transaction // served instead of paging after the first fetch
	aspsps      []ASPSPData   // served for GET /aspsps
	lastAuthReq StartAuthorizationRequest
	expectKID   string // jwt kid the fake accepts (default test-app)
}

// setSessionErr toggles the session failure for GET /sessions/{id}
// (mutex-guarded: the fake's handler runs on the server goroutine).
func (f *fakeEB) setSessionErr(e *APIError) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sessionErr = e
}

func (f *fakeEB) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") {
			f.t.Error("missing Bearer token")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		// Decode the token to confirm our JWT is sent (and is self-consistent).
		parts := strings.Split(strings.TrimPrefix(auth, "Bearer "), ".")
		if len(parts) != 3 {
			f.t.Errorf("malformed JWT: %q", auth)
			http.Error(w, "bad jwt", http.StatusUnauthorized)
			return
		}
		kid := extractKID(f.t, parts[0])
		want := f.expectKID
		if want == "" {
			want = "test-app"
		}
		if kid != want {
			f.t.Errorf("JWT kid = %q, want %q", kid, want)
		}

		if f.statusCode != 0 {
			w.WriteHeader(f.statusCode)
			_, _ = w.Write([]byte(f.errBody))
			return
		}

		switch {
		case r.Method == http.MethodDelete && r.URL.Path == "/sessions/sesh-1":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"message":"OK"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/aspsps":
			if f.aspsps == nil {
				f.aspsps = []ASPSPData{{Name: "Mock ASPSP", Country: "FI"}}
			}
			writeJSON(w, GetASPSPsResponse{ASPSPs: f.aspsps})
		case r.Method == http.MethodPost && r.URL.Path == "/auth":
			var req StartAuthorizationRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				f.t.Errorf("decode /auth body: %v", err)
			}
			if req.Aspsp.Name == "" || req.State == "" || req.RedirectURL == "" {
				f.t.Errorf("incomplete /auth request: %+v", req)
			}
			f.mu.Lock()
			f.lastAuthReq = req
			f.mu.Unlock()
			writeJSON(w, StartAuthorizationResponse{URL: f.authURL, AuthorizationID: "authid-1"})
		case r.Method == http.MethodPost && r.URL.Path == "/sessions":
			var req AuthorizeSessionRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			if req.Code != "code-123" {
				f.t.Errorf("session code = %q, want code-123", req.Code)
			}
			writeJSON(w, f.sessionResp)
		case r.Method == http.MethodGet && r.URL.Path == "/sessions/sesh-1":
			f.mu.Lock()
			se := f.sessionErr
			f.mu.Unlock()
			if se != nil {
				w.WriteHeader(se.HTTPStatus)
				_ = json.NewEncoder(w).Encode(ErrorResponse{Message: se.Message, Code: se.HTTPStatus, Error: se.Code, Detail: se.Detail})
				return
			}
			writeJSON(w, GetSessionResponse{
				Status:   SessionAuthorized,
				Accounts: []string{f.sessionResp.Accounts[0].UID},
				AccountsData: []SessionAccount{{
					UID: f.sessionResp.Accounts[0].UID, IdentificationHash: "hash-1",
				}},
				Access: Access{ValidUntil: "2025-08-01T00:00:00Z"},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/accounts/"+f.sessionResp.Accounts[0].UID+"/details":
			writeJSON(w, f.details)
		case r.Method == http.MethodGet && r.URL.Path == "/accounts/"+f.sessionResp.Accounts[0].UID+"/balances":
			writeJSON(w, f.balances)
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/accounts/"+f.sessionResp.Accounts[0].UID+"/transactions"):
			f.mu.Lock()
			f.txnCalls++
			first, after := f.txnCalls == 1, f.txnsAfter
			f.mu.Unlock()
			switch {
			case !first && after != nil:
				writeJSON(w, TransactionsResponse{Transactions: after})
			case r.URL.Query().Get("continuation_key") == "":
				writeJSON(w, f.txnsPage1)
			default:
				writeJSON(w, f.txnsPage2)
			}
		default:
			f.t.Errorf("unexpected request: %s %s", r.Method, r.URL.String())
			http.Error(w, "not found", http.StatusNotFound)
		}
	})
}

func extractKID(t *testing.T, part string) string {
	t.Helper()
	b, err := base64.RawURLEncoding.DecodeString(part)
	if err != nil {
		t.Fatalf("decode jwt header: %v", err)
	}
	var h struct {
		Kid string `json:"kid"`
	}
	if err := json.Unmarshal(b, &h); err != nil {
		t.Fatal(err)
	}
	return h.Kid
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func testClient(t *testing.T, f *fakeEB) *Client {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f.t = t // handlers report on the owning test's *testing.T
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	return NewClient(srv.URL, "test-app", key)
}

func TestClientAuthorizationAndPaging(t *testing.T) {
	f := &fakeEB{
		authURL: "https://auth.invalid/start",
		sessionResp: AuthorizeSessionResponse{
			SessionID: "sesh-1",
			Accounts: []Account{{
				AccountID: AccountIdentification{Iban: "FI123456"}, Name: "Mock",
				CashAccountType: "CACC", Currency: "EUR",
				UID: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", IdentificationHash: "hash-1",
			}},
		},
		details: Account{
			AccountID: AccountIdentification{Iban: "FI123456"}, Name: "Mock",
			Product: "Mock Account", Currency: "EUR",
		},
		balances: BalancesResponse{Balances: []BalanceResource{{
			Name: "Booked", BalanceAmount: Amount{Currency: "EUR", Amount: "1234.56"}, BalanceType: "CLBD",
		}}},
		txnsPage1: TransactionsResponse{
			Transactions: []Transaction{{
				EntryReference: "t1", TransactionAmount: Amount{Currency: "EUR", Amount: "12.34"},
				CreditDebitIndicator: "DBIT", Status: "BOOK", BookingDate: "2025-01-02",
			}},
			ContinuationKey: strPtr("next-page"),
		},
		txnsPage2: TransactionsResponse{
			Transactions: []Transaction{{
				EntryReference: "t2", TransactionAmount: Amount{Currency: "EUR", Amount: "-5.00"},
				CreditDebitIndicator: "CRDT", Status: "BOOK", BookingDate: "2025-01-01",
			}},
		},
	}
	c := testClient(t, f)
	ctx := context.Background()

	resp, err := c.StartAuthorization(ctx, StartAuthorizationRequest{
		Access: Access{ValidUntil: "2025-08-01T00:00:00Z"},
		Aspsp:  ASPSP{Name: "Mock ASPSP", Country: "FI"},
		State:  "state-1", RedirectURL: "http://localhost:8080/links/callback", PSUType: "personal",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.URL != f.authURL {
		t.Errorf("auth URL = %q", resp.URL)
	}

	auth, err := c.AuthorizeSession(ctx, "code-123")
	if err != nil {
		t.Fatal(err)
	}
	if auth.SessionID != "sesh-1" || len(auth.Accounts) != 1 {
		t.Fatalf("session wrong: %+v", auth)
	}

	txns, err := c.GetTransactions(ctx, auth.Accounts[0].UID, "2025-01-01", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(txns) != 2 || txns[1].EntryReference != "t2" {
		t.Errorf("paging broken: %+v", txns)
	}

	bals, err := c.GetBalances(ctx, auth.Accounts[0].UID)
	if err != nil || len(bals) != 1 {
		t.Fatalf("balances: %v %+v", err, bals)
	}
	if bals[0].BalanceAmount.Amount != "1234.56" {
		t.Errorf("balance amount = %q", bals[0].BalanceAmount.Amount)
	}
}

func TestClientErrorEnvelope(t *testing.T) {
	f := &fakeEB{
		statusCode: http.StatusUnprocessableEntity,
		errBody:    `{"message":"Required PSU header is not provided","code":422,"error":"PSU_HEADER_NOT_PROVIDED","detail":"psuIpAddress"}`,
	}
	c := testClient(t, f)
	_, err := c.ListASPSPs(context.Background(), "FI")
	ae, ok := err.(*APIError)
	if !ok {
		t.Fatalf("err = %T, want *APIError: %v", err, err)
	}
	if ae.Code != "PSU_HEADER_NOT_PROVIDED" || ae.HTTPStatus != 422 {
		t.Errorf("bad APIError: %+v", ae)
	}
	if !strings.Contains(ae.Error(), "psuIpAddress") {
		t.Errorf("detail missing from error string: %v", ae)
	}
}

func TestIsSessionError(t *testing.T) {
	for code, want := range map[string]bool{
		"EXPIRED_SESSION": true, "REVOKED_SESSION": true, "CLOSED_SESSION": true,
		"WRONG_REQUEST_PARAMETERS": false,
	} {
		err := &APIError{Code: code}
		if IsSessionError(err) != want {
			t.Errorf("IsSessionError(%q) = %v, want %v", code, !want, want)
		}
	}
	if IsSessionError(context.Canceled) {
		t.Error("non-APIError reported as session error")
	}
}

func strPtr(s string) *string { return &s }

var _ = url.QueryEscape // keep net/url import (used in client)

// TestDynamicClientCredentialSwitch: a client built with NewDynamicClient
// re-signs with whatever the resolver returns — no fixed identity.
func TestDynamicClientCredentialSwitch(t *testing.T) {
	key1, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	key2, _ := rsa.GenerateKey(rand.Reader, 2048)

	current := Credentials{BaseURL: "", AppID: "", PrivateKey: nil}
	c := NewDynamicClient(func() (Credentials, error) {
		if current.AppID == "" {
			return Credentials{}, errors.New("credentials not configured yet")
		}
		return current, nil
	})

	f := &fakeEB{aspsps: []ASPSPData{{Name: "Mock ASPSP", Country: "FI"}}}
	f.t = t
	srv := httptest.NewServer(f.handler())
	defer srv.Close()
	c.http = srv.Client()
	base := srv.URL

	// Resolver error surfaces without any request.
	if _, err := c.ListASPSPs(context.Background(), "FI"); err == nil || !strings.Contains(err.Error(), "not configured yet") {
		t.Errorf("resolver error not surfaced: %v", err)
	}

	// Credential switch: key + kid change between calls.
	current = Credentials{BaseURL: base, AppID: "app-one", PrivateKey: key1}
	f.expectKID = "app-one"
	if _, err := c.ListASPSPs(context.Background(), "FI"); err != nil {
		t.Fatal(err)
	}
	current = Credentials{BaseURL: base, AppID: "app-two", PrivateKey: key2}
	f.expectKID = "app-two"
	if _, err := c.ListASPSPs(context.Background(), "FI"); err != nil {
		t.Fatal(err)
	}
}
