package auth

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"testing"
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
