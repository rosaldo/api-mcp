package auth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
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
//	{timestamp}           computed per request; format set by TimestampFormat
//	{method}              the HTTP verb, uppercase (GET, POST…)
//	{body} {path} {query} the request itself ({query} sorted, `k=v` joined, no separator)
//	{signature}           the result (Into only)
//
// The same {timestamp} value is used in the payload and in Into, so a scheme that signs the
// timestamp AND sends it in a header stays consistent — sign one instant and send another and
// every call fails with a signature error that says nothing about clocks.
type Signature struct {
	// Algo is `sha256` (plain hash of the payload) or `hmac-sha256` (keyed by Secret).
	Algo string
	// Payload is the template of the string to sign.
	Payload string
	// Into says where the signature goes: `header:Name=template` or `query:name=template`.
	Into string
	// Encoding is how the signature bytes become text: `hex` (default) or `base64`. Both are
	// common; picking the wrong one fails every call with an authentication error that never
	// mentions encoding.
	Encoding string
	// TimestampFormat is what {timestamp} expands to: `unix` (default, seconds since the epoch)
	// or `iso8601-ms` (2020-12-08T09:08:57.715Z). APIs that reject a request whose timestamp
	// drifts by more than a few seconds usually want the second one.
	TimestampFormat string

	AppID  string
	Secret string
}

// stamp renders the current instant in the configured format.
func (s Signature) stamp(now time.Time) string {
	if strings.EqualFold(s.TimestampFormat, "iso8601-ms") {
		return now.UTC().Format("2006-01-02T15:04:05.000Z")
	}
	return strconv.FormatInt(now.Unix(), 10)
}

func (s Signature) Apply(_ context.Context, req *http.Request) error {
	body, err := bodyOf(req)
	if err != nil {
		return fmt.Errorf("signing: reading the body: %w", err)
	}

	fields := map[string]string{
		"{app_id}":    s.AppID,
		"{secret}":    s.Secret,
		"{timestamp}": s.stamp(time.Now()),
		"{method}":    strings.ToUpper(req.Method),
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
	var raw []byte
	switch strings.ToLower(s.Algo) {
	case "sha256":
		sum := sha256.Sum256([]byte(payload))
		raw = sum[:]
	case "hmac-sha256":
		mac := hmac.New(sha256.New, []byte(s.Secret))
		mac.Write([]byte(payload))
		raw = mac.Sum(nil)
	default:
		return "", fmt.Errorf("signing: unknown algorithm %q (use sha256 or hmac-sha256)", s.Algo)
	}
	return s.encode(raw)
}

// encode turns the signature bytes into the text the API expects. Default is hex, which is what
// this tool did before the option existed — an existing configuration keeps working untouched.
func (s Signature) encode(raw []byte) (string, error) {
	switch strings.ToLower(s.Encoding) {
	case "", "hex":
		return hex.EncodeToString(raw), nil
	case "base64":
		return base64.StdEncoding.EncodeToString(raw), nil
	default:
		return "", fmt.Errorf("signing: unknown encoding %q (use hex or base64)", s.Encoding)
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
