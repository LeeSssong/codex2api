package turnstate

import (
	"encoding/base64"
	"encoding/binary"
	"strings"
	"testing"
	"time"
)

func encodedTicket(t *testing.T, decodedLen int, issued time.Time) string {
	t.Helper()
	raw := make([]byte, decodedLen)
	raw[0] = 0x80
	binary.BigEndian.PutUint64(raw[1:9], uint64(issued.Unix()))
	return base64.URLEncoding.EncodeToString(raw)
}

func TestParseAcceptsQualifiedTicketShapes(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	for _, decodedLen := range []int{217, 249} {
		raw := encodedTicket(t, decodedLen, now.Add(-time.Minute))
		got, err := Parse(raw, now)
		if err != nil {
			t.Fatalf("Parse(%d bytes): %v", decodedLen, err)
		}
		if got.Raw != raw || got.DecodedLength != decodedLen {
			t.Fatalf("ticket = %#v", got)
		}
		if got.ExpiresAt.Sub(got.IssuedAt) != Lifetime {
			t.Fatalf("lifetime = %v", got.ExpiresAt.Sub(got.IssuedAt))
		}
	}
}

func TestParseRejectsInvalidShapeAndTime(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	cases := map[string]string{
		"312 length": strings.Repeat("a", 312),
		"356 length": strings.Repeat("a", 356),
		"expired":    encodedTicket(t, 217, now.Add(-Lifetime)),
		"future":     encodedTicket(t, 217, now.Add(FutureSkew+time.Second)),
		"bad base64": strings.Repeat("!", 292),
	}
	wrongPrefix := make([]byte, 217)
	binary.BigEndian.PutUint64(wrongPrefix[1:9], uint64(now.Unix()))
	cases["wrong prefix"] = base64.URLEncoding.EncodeToString(wrongPrefix)
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse(raw, now); err == nil {
				t.Fatal("Parse succeeded, want error")
			}
		})
	}
}

func TestCredentialHashSeparatesTokenAndAccount(t *testing.T) {
	a := CredentialHash("token-a", "acct")
	if a == CredentialHash("token-b", "acct") || a == CredentialHash("token-a", "other") {
		t.Fatal("credential hash did not isolate credentials")
	}
}
