package auth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Signature is per-request authentication: instead of carrying a fixed token, each call is
// signed over its own content. Shopee's affiliate API and TikTok Shop's both work this way, and
// no amount of bearer/apikey configuration reaches them — the credential is not a value, it is
// a computation.
//
// The shapes differ (Shopee hashes appId+timestamp+body+secret into an Authorization header;
// TikTok HMACs path+query+body into a `sign` query parameter), so what is configurable here is
// the RECIPE: which algorithm, which string gets signed, and where the result goes.
//
// Placeholders, usable in both Payload and Into:
//
//	{app_id} {secret}     the credentials
//	{timestamp}           unix seconds, computed per request
//	{body} {path} {query} the request itself ({query} sorted, `k=v` joined, no separator)
//	{signature}           the result (Into only)
type Signature struct {
	// Algo is `sha256` (plain hash of the payload) or `hmac-sha256` (keyed by Secret).
	Algo string
	// Payload is the template of the string to sign.
	Payload string
	// Into says where the signature goes: `header:Name=template` or `query:name=template`.
	Into string

	AppID  string
	Secret string
}

func (s Signature) Apply(_ context.Context, req *http.Request) error {
	body, err := bodyOf(req)
	if err != nil {
		return fmt.Errorf("signing: reading the body: %w", err)
	}

	fields := map[string]string{
		"{app_id}":    s.AppID,
		"{secret}":    s.Secret,
		"{timestamp}": strconv.FormatInt(time.Now().Unix(), 10),
		"{body}":      string(body),
		"{path}":      req.URL.Path,
		"{query}":     sortedQuery(req),
	}

	signed, err := s.sign(expand(s.Payload, fields))
	if err != nil {
		return err
	}
	fields["{signature}"] = signed

	where, rest, ok := strings.Cut(s.Into, ":")
	if !ok {
		return fmt.Errorf("signing: --sign-into must be header:Name=template or query:name=template")
	}
	name, template, ok := strings.Cut(rest, "=")
	if !ok {
		return fmt.Errorf("signing: --sign-into must be header:Name=template or query:name=template")
	}
	value := expand(template, fields)

	switch strings.ToLower(where) {
	case "header":
		req.Header.Set(name, value)
	case "query":
		q := req.URL.Query()
		q.Set(name, value)
		req.URL.RawQuery = q.Encode()
	default:
		return fmt.Errorf("signing: don't know how to put the signature in %q (use header or query)", where)
	}
	return nil
}

func (s Signature) sign(payload string) (string, error) {
	switch strings.ToLower(s.Algo) {
	case "sha256":
		sum := sha256.Sum256([]byte(payload))
		return hex.EncodeToString(sum[:]), nil
	case "hmac-sha256":
		mac := hmac.New(sha256.New, []byte(s.Secret))
		mac.Write([]byte(payload))
		return hex.EncodeToString(mac.Sum(nil)), nil
	default:
		return "", fmt.Errorf("signing: unknown algorithm %q (use sha256 or hmac-sha256)", s.Algo)
	}
}

func expand(template string, fields map[string]string) string {
	out := template
	for placeholder, value := range fields {
		out = strings.ReplaceAll(out, placeholder, value)
	}
	return out
}

// bodyOf reads the request body and puts it back, because the signature covers it and the
// request still has to be sent. `GetBody` is what makes this safe: net/http sets it for the
// readers we build, and it hands back a fresh reader every time.
func bodyOf(req *http.Request) ([]byte, error) {
	if req.Body == nil {
		return nil, nil
	}
	if req.GetBody != nil {
		fresh, err := req.GetBody()
		if err != nil {
			return nil, err
		}
		defer fresh.Close() //nolint:errcheck
		return io.ReadAll(fresh)
	}
	data, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	req.Body = io.NopCloser(strings.NewReader(string(data)))
	return data, nil
}

// sortedQuery renders the query string the way signing schemes expect it: keys in order, `k=v`
// concatenated with no separator. Order matters — the server rebuilds the same string, and a
// different order is a different signature.
func sortedQuery(req *http.Request) string {
	values := req.URL.Query()
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k)
		b.WriteString("=")
		b.WriteString(values.Get(k))
	}
	return b.String()
}
