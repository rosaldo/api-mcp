package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// The flow is why this package exists: a short-lived token, renewed on its own. Ask for a new
// token on every call and the API gets hammered until the rate limit trips; never renew and the
// server works until the token expires, then returns 401 forever.
func TestFlowReusesTokenAndRenewsBeforeExpiry(t *testing.T) {
	var requests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		if r.Form.Get("key") == "" || r.Form.Get("secret") == "" {
			t.Errorf("credentials did not arrive: %v", r.Form)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","data":{"token":"tok-` + string(rune('0'+requests)) + `"}}`))
	}))
	defer srv.Close()

	f := &Flow{
		URL:       srv.URL,
		Fields:    map[string]string{"key": "k", "secret": "s"},
		TokenPath: []string{"data", "token"},
		TTL:       2 * time.Hour,
	}

	req := emptyRequest(t)
	for i := 0; i < 5; i++ {
		if err := f.Apply(context.Background(), req); err != nil {
			t.Fatalf("Apply: %v", err)
		}
	}
	if requests != 1 {
		t.Errorf("authenticated %d times across 5 calls — the token is not being reused", requests)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer tok-1" {
		t.Errorf("Authorization = %q", got)
	}

	// Expired: the next call has to fetch another one.
	f.Invalidate()
	if err := f.Apply(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if requests != 2 {
		t.Errorf("after invalidating, authenticated %d times (want 2)", requests)
	}
}

// The margin exists so the token is never used in its last second of life: between our call and
// its arrival at the API there is a network, and the clock on the other side is never ours.
func TestFlowRenewsWithinMargin(t *testing.T) {
	var requests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		_, _ = w.Write([]byte(`{"data":{"token":"t"}}`))
	}))
	defer srv.Close()

	f := &Flow{URL: srv.URL, TokenPath: []string{"data", "token"}, TTL: time.Minute}
	if err := f.Apply(context.Background(), emptyRequest(t)); err != nil {
		t.Fatal(err)
	}
	// A 1-minute TTL means a 30s minimum margin. A token with 20s of life left is INSIDE the
	// margin and must be replaced, not used.
	f.expiresAt = time.Now().Add(20 * time.Second)
	if err := f.Apply(context.Background(), emptyRequest(t)); err != nil {
		t.Fatal(err)
	}
	if requests != 2 {
		t.Errorf("a token inside the margin was reused (%d authentications)", requests)
	}
}

// An authentication error must NOT carry the credential with it: whoever answers a token
// request tends to echo back what it received, and what it received is key+secret.
func TestAuthErrorDoesNotLeakCredential(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid key: USER-SECRET"}`))
	}))
	defer srv.Close()

	f := &Flow{URL: srv.URL, Fields: map[string]string{"secret": "USER-SECRET"}, TokenPath: []string{"t"}, TTL: time.Hour}
	err := f.Apply(context.Background(), emptyRequest(t))
	if err == nil {
		t.Fatal("401 passed as success")
	}
	if strings.Contains(err.Error(), "USER-SECRET") {
		t.Errorf("the credential leaked into the error message: %v", err)
	}
}

func TestAPIKeyGoesWhereTold(t *testing.T) {
	cases := []struct{ in, name, value string }{
		{"header", "X-Token", "abc"},
		{"query", "token", "abc"},
		{"cookie", "sid", "abc"},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			req := emptyRequest(t)
			if err := (APIKey{In: c.in, Name: c.name, Value: c.value}).Apply(context.Background(), req); err != nil {
				t.Fatal(err)
			}
			switch c.in {
			case "header":
				if req.Header.Get(c.name) != c.value {
					t.Error("did not reach the header")
				}
			case "query":
				if req.URL.Query().Get(c.name) != c.value {
					t.Error("did not reach the query string")
				}
			case "cookie":
				if ck, err := req.Cookie(c.name); err != nil || ck.Value != c.value {
					t.Error("did not reach the cookie")
				}
			}
		})
	}
	if err := (APIKey{In: "whatever", Name: "x", Value: "y"}).Apply(context.Background(), emptyRequest(t)); err == nil {
		t.Error("an unknown location passed silently — the key would go nowhere")
	}
}

func emptyRequest(t *testing.T) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, "https://api.example.test/x", nil)
	if err != nil {
		t.Fatal(err)
	}
	return req
}

// TestRequestIDIsFreshPerCall pins the reason `{uuid}` exists: an API that demands a unique id
// per request gets a different one every time, and a header that asked for nothing is left alone.
func TestRequestIDIsFreshPerCall(t *testing.T) {
	seen := map[string]bool{}
	for range 3 {
		req, err := http.NewRequest("GET", "https://public-api.etoro.com/api/v1/watchlists", nil)
		if err != nil {
			t.Fatal(err)
		}
		// What the dialects do: a literal value with a placeholder inside.
		req.Header.Set("x-request-id", "{uuid}")
		req.Header.Set("x-api-key", "fixed-key")

		if err := (RequestID{}).Apply(context.Background(), req); err != nil {
			t.Fatalf("Apply: %v", err)
		}

		id := req.Header.Get("x-request-id")
		if strings.Contains(id, "{") {
			t.Fatalf("the placeholder reached the server as text: %q", id)
		}
		if _, err := uuid.Parse(id); err != nil {
			t.Fatalf("%q is not a uuid: %v", id, err)
		}
		if seen[id] {
			t.Fatalf("the same id twice: %q — a unique-id header that repeats is worse than none", id)
		}
		seen[id] = true

		if got := req.Header.Get("x-api-key"); got != "fixed-key" {
			t.Errorf("a plain header was rewritten: %q", got)
		}
	}
}

// TestChainRunsBothAppliers proves the id is filled for an API that signs nothing (auth.None is
// what eToro gets) and that a later applier still sees the request.
func TestChainRunsBothAppliers(t *testing.T) {
	req, err := http.NewRequest("GET", "https://public-api.etoro.com/api/v1/watchlists", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("x-request-id", "{uuid}")

	chain := Chain{RequestID{}, Bearer{Token: "t"}}
	if err := chain.Apply(context.Background(), req); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if _, err := uuid.Parse(req.Header.Get("x-request-id")); err != nil {
		t.Errorf("the chain did not fill the id: %v", err)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer t" {
		t.Errorf("the chain stopped early: Authorization = %q", got)
	}
}
