// Package auth decides how a request identifies itself to the API.
//
// There are two families, and the second is why this package exists. STATIC is what every tool
// does: a bearer, an API key, a basic — a fixed value, from a flag, applied to every call.
// DYNAMIC is what many production APIs require: trading key+secret for a short-lived JWT,
// typically valid for an hour or two. With static auth, an MCP server for those APIs works
// until the token expires and then returns 401 until somebody restarts it.
package auth

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Applier stamps identity onto a request that is about to go out.
type Applier interface {
	Apply(ctx context.Context, req *http.Request) error
}

// None authenticates nothing — public APIs exist.
type None struct{}

func (None) Apply(context.Context, *http.Request) error { return nil }

// Bearer sets `Authorization: Bearer <token>`.
type Bearer struct{ Token string }

func (b Bearer) Apply(_ context.Context, req *http.Request) error {
	req.Header.Set("Authorization", "Bearer "+b.Token)
	return nil
}

// Basic sets `Authorization: Basic <base64(user:password)>`.
type Basic struct{ User, Password string }

func (b Basic) Apply(_ context.Context, req *http.Request) error {
	req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(b.User+":"+b.Password)))
	return nil
}

// APIKey puts the key where the API expects it: header, query or cookie.
type APIKey struct {
	In    string // header | query | cookie
	Name  string
	Value string
}

func (k APIKey) Apply(_ context.Context, req *http.Request) error {
	switch strings.ToLower(k.In) {
	case "header":
		req.Header.Set(k.Name, k.Value)
	case "query":
		q := req.URL.Query()
		q.Set(k.Name, k.Value)
		req.URL.RawQuery = q.Encode()
	case "cookie":
		req.AddCookie(&http.Cookie{Name: k.Name, Value: k.Value})
	default:
		return fmt.Errorf("api key: don't know how to put it in %q (use header, query or cookie)", k.In)
	}
	return nil
}

// Flow is dynamic auth: it trades credentials for a short-lived token and renews it on its own.
//
// The token is kept in memory and renewed BEFORE it expires, with a margin. Renewing on a 401
// would also work, but it burns one call every couple of hours and turns a predictable event
// into a visible failure for whoever is on the other side.
type Flow struct {
	// URL of the endpoint that issues the token.
	URL string
	// Fields sent as form-urlencoded, which is what authentication endpoints usually accept.
	Fields map[string]string
	// TokenPath says where the token sits in the JSON response, as nested keys.
	// E.g. {"data":{"token":"x"}} → []string{"data","token"}.
	TokenPath []string
	// TTL is how long the token is valid. The renewal margin is derived from it.
	TTL time.Duration
	// Client is injectable for tests.
	Client *http.Client

	mu        sync.Mutex
	token     string
	expiresAt time.Time
}

// margin is how long before expiry the token gets replaced. 10% of the TTL, at least 30s:
// renewing exactly at expiry is a race against the server's clock, which is never ours.
func (f *Flow) margin() time.Duration {
	m := f.TTL / 10
	if m < 30*time.Second {
		m = 30 * time.Second
	}
	return m
}

func (f *Flow) Apply(ctx context.Context, req *http.Request) error {
	tok, err := f.get(ctx)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	return nil
}

// Invalidate throws away the stored token — for when the API answered 401 despite the validity
// it announced itself. The next call authenticates again.
func (f *Flow) Invalidate() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.token, f.expiresAt = "", time.Time{}
}

func (f *Flow) get(ctx context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.token != "" && time.Now().Before(f.expiresAt.Add(-f.margin())) {
		return f.token, nil
	}

	form := url.Values{}
	for k, v := range f.Fields {
		form.Set(k, v)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.URL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client := f.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("authenticating: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck
	var body bytes.Buffer
	if _, err := body.ReadFrom(resp.Body); err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		// The body does NOT go into the error: whoever answers a token request tends to echo
		// back what it received, and what it received is the credential.
		return "", fmt.Errorf("authenticating: %s returned %s", f.URL, resp.Status)
	}

	var doc map[string]any
	if err := json.Unmarshal(body.Bytes(), &doc); err != nil {
		return "", fmt.Errorf("authenticating: response is not JSON: %w", err)
	}
	tok, err := extract(doc, f.TokenPath)
	if err != nil {
		return "", err
	}
	f.token = tok
	f.expiresAt = time.Now().Add(f.TTL)
	return tok, nil
}

// extract walks the JSON down the given keys until it finds the token string.
func extract(doc map[string]any, path []string) (string, error) {
	if len(path) == 0 {
		return "", fmt.Errorf("authenticating: nobody told me where the token is in the response")
	}
	var current any = doc
	for _, key := range path {
		m, ok := current.(map[string]any)
		if !ok {
			return "", fmt.Errorf("authenticating: %q is not an object along the token path", key)
		}
		current, ok = m[key]
		if !ok {
			return "", fmt.Errorf("authenticating: the response has no %q", strings.Join(path, "."))
		}
	}
	tok, ok := current.(string)
	if !ok || tok == "" {
		return "", fmt.Errorf("authenticating: %s did not yield a non-empty string", strings.Join(path, "."))
	}
	return tok, nil
}
