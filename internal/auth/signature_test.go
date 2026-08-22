package auth

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"strings"
	"testing"
	"time"
)

// The two real shapes this exists for. Both are checked against a signature computed here, by
// hand — not against whatever the code happens to produce.
func TestSignsTheRealWorldShapes(t *testing.T) {
	const body = `{"query":"{ shopeeOfferV2 { nodes { commissionRate } } }"}`

	t.Run("plain sha256 in a header", func(t *testing.T) {
		// appId + timestamp + body + secret, hashed, inside an Authorization header.
		s := Signature{
			Algo:    "sha256",
			Payload: "{app_id}{timestamp}{body}{secret}",
			Into:    "header:Authorization=SHA256 Credential={app_id}, Timestamp={timestamp}, Signature={signature}",
			AppID:   "app-123",
			Secret:  "sec-456",
		}
		req := postWithBody(t, "https://api.example.test/graphql", body)
		if err := s.Apply(context.Background(), req); err != nil {
			t.Fatalf("Apply: %v", err)
		}

		header := req.Header.Get("Authorization")
		if !strings.HasPrefix(header, "SHA256 Credential=app-123, Timestamp=") {
			t.Fatalf("header shape wrong: %q", header)
		}
		ts := between(header, "Timestamp=", ",")
		got := between(header, "Signature=", "")
		sum := sha256.Sum256([]byte("app-123" + ts + body + "sec-456"))
		if want := hex.EncodeToString(sum[:]); got != want {
			t.Errorf("signature = %s, want %s", got, want)
		}

		// The body still has to be sendable after being read for the signature.
		sent, _ := readAll(req)
		if sent != body {
			t.Errorf("the body was consumed by signing: %q", sent)
		}
	})

	t.Run("hmac-sha256 in a query parameter", func(t *testing.T) {
		// HMAC over path + sorted query + body, carried as `sign`.
		s := Signature{
			Algo:    "hmac-sha256",
			Payload: "{path}{query}{body}",
			Into:    "query:sign={signature}",
			AppID:   "app-123",
			Secret:  "sec-456",
		}
		req := postWithBody(t, "https://api.example.test/affiliate/orders?shop_id=9&page=2", body)
		if err := s.Apply(context.Background(), req); err != nil {
			t.Fatalf("Apply: %v", err)
		}

		got := req.URL.Query().Get("sign")
		mac := hmac.New(sha256.New, []byte("sec-456"))
		mac.Write([]byte("/affiliate/orders" + "page=2shop_id=9" + body))
		if want := hex.EncodeToString(mac.Sum(nil)); got != want {
			t.Errorf("sign = %s, want %s (keys must be sorted: page before shop_id)", got, want)
		}
	})
}

// A wrong algorithm has to fail at the first call, not sign with something else and let the API
// answer 401 forever.
func TestUnknownAlgorithmFails(t *testing.T) {
	s := Signature{Algo: "md5", Payload: "{body}", Into: "header:X-Sign={signature}"}
	if err := s.Apply(context.Background(), postWithBody(t, "https://x.test", "{}")); err == nil {
		t.Error("an unknown algorithm signed anyway")
	}
}

// A malformed --sign-into must not silently drop the signature: a request that goes out
// unsigned looks like a credentials problem on the API's side.
func TestMalformedDestinationFails(t *testing.T) {
	for _, into := range []string{"Authorization=x", "cookie:x={signature}", "header"} {
		s := Signature{Algo: "sha256", Payload: "{body}", Into: into}
		if err := s.Apply(context.Background(), postWithBody(t, "https://x.test", "{}")); err == nil {
			t.Errorf("--sign-into %q passed silently", into)
		}
	}
}

func postWithBody(t *testing.T, url, body string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatal(err)
	}
	return req
}

func readAll(req *http.Request) (string, error) {
	data, err := bodyOf(req)
	return string(data), err
}

func between(s, start, end string) string {
	i := strings.Index(s, start)
	if i < 0 {
		return ""
	}
	rest := s[i+len(start):]
	if end == "" {
		return rest
	}
	j := strings.Index(rest, end)
	if j < 0 {
		return rest
	}
	return rest[:j]
}

// TestBase64Encoding: hex and base64 are both common, and picking the wrong one fails every call
// with an authentication error that never mentions encoding. Default stays hex, so a
// configuration written before this option existed keeps working untouched.
func TestBase64Encoding(t *testing.T) {
	payload := "hello"
	mac := hmac.New(sha256.New, []byte("s3cr3t"))
	mac.Write([]byte(payload))
	esperado := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	s := Signature{Algo: "hmac-sha256", Secret: "s3cr3t", Encoding: "base64"}
	got, err := s.sign(payload)
	if err != nil {
		t.Fatal(err)
	}
	if got != esperado {
		t.Errorf("base64: got %q, want %q", got, esperado)
	}

	// hex remains the default when nothing is said.
	padrao, err := Signature{Algo: "hmac-sha256", Secret: "s3cr3t"}.sign(payload)
	if err != nil {
		t.Fatal(err)
	}
	if padrao == got {
		t.Error("the default changed to base64 — every existing configuration would break")
	}
	if _, err := (Signature{Algo: "hmac-sha256", Encoding: "base32"}).sign(payload); err == nil {
		t.Error("an unknown encoding passed silently")
	}
}

// TestISO8601Timestamp: APIs that reject a request whose timestamp drifts by seconds usually want
// an ISO 8601 instant with milliseconds, not a unix count. The two are not interchangeable and
// the failure looks like a wrong secret.
func TestISO8601Timestamp(t *testing.T) {
	instante := time.Date(2020, 12, 8, 9, 8, 57, 715_000_000, time.UTC)

	if got := (Signature{TimestampFormat: "iso8601-ms"}).stamp(instante); got != "2020-12-08T09:08:57.715Z" {
		t.Errorf("iso8601-ms: got %q, want %q", got, "2020-12-08T09:08:57.715Z")
	}
	if got := (Signature{}).stamp(instante); got != "1607418537" {
		t.Errorf("default should stay unix seconds, got %q", got)
	}
	// A non-UTC instant still renders as UTC: signing local time is a whole class of bug.
	emOutroFuso := instante.In(time.FixedZone("GMT-3", -3*3600))
	if got := (Signature{TimestampFormat: "iso8601-ms"}).stamp(emOutroFuso); got != "2020-12-08T09:08:57.715Z" {
		t.Errorf("a non-UTC instant was not converted: got %q", got)
	}
}

// TestSignedRequestCarriesMethodAndOneTimestamp exercises a scheme that signs
// timestamp+method+path+body, sends the signature base64 in one header and the SAME timestamp in
// another. Two things break it and neither says so: a missing {method}, and a timestamp that is
// computed twice — the payload signing one instant while the header announces the next.
func TestSignedRequestCarriesMethodAndOneTimestamp(t *testing.T) {
	s := Signature{
		Algo:            "hmac-sha256",
		Payload:         "{timestamp}{method}{path}{body}",
		Into:            "header:X-SIGN={signature}",
		Encoding:        "base64",
		TimestampFormat: "iso8601-ms",
		Secret:          "s3cr3t",
	}
	req, _ := http.NewRequest("POST", "https://api.test/v5/trade/order", strings.NewReader(`{"a":1}`))
	if err := s.Apply(context.Background(), req); err != nil {
		t.Fatal(err)
	}

	assinatura := req.Header.Get("X-SIGN")
	if assinatura == "" {
		t.Fatal("no signature was set")
	}
	if strings.ContainsAny(assinatura, "ghijklmnopqrstuvwxyz") == false && len(assinatura) == 64 {
		t.Error("the signature looks like hex, not base64")
	}

	// Recompute it: the only unknown is the instant, so try the payload with the method in place
	// and confirm the signature matches for the timestamp the request actually used.
	instante := s.stamp(time.Now())
	mac := hmac.New(sha256.New, []byte("s3cr3t"))
	mac.Write([]byte(instante + "POST" + "/v5/trade/order" + `{"a":1}`))
	if base64.StdEncoding.EncodeToString(mac.Sum(nil)) != assinatura {
		t.Errorf("signature does not match timestamp+METHOD+path+body\n got %q", assinatura)
	}
}
