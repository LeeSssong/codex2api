package turnstate

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	HeaderName   = "X-Codex-Turn-State"
	HarvestModel = "gpt-6-astra"
	Lifetime     = 3570 * time.Second
	RenewAfter   = 3000 * time.Second
	FutureSkew   = 30 * time.Second
)

var ErrInvalidTicket = errors.New("invalid codex turn-state ticket")

type Ticket struct {
	Raw           string
	IssuedAt      time.Time
	ExpiresAt     time.Time
	DecodedLength int
}

func Parse(raw string, now time.Time) (Ticket, error) {
	raw = strings.TrimSpace(raw)
	if len(raw) != 292 && len(raw) != 332 {
		return Ticket{}, fmt.Errorf("%w: encoded length %d", ErrInvalidTicket, len(raw))
	}
	decoded, err := base64.URLEncoding.DecodeString(raw)
	if err != nil {
		decoded, err = base64.RawURLEncoding.DecodeString(raw)
	}
	if err != nil {
		return Ticket{}, fmt.Errorf("%w: base64url", ErrInvalidTicket)
	}
	if len(decoded) != 217 && len(decoded) != 249 {
		return Ticket{}, fmt.Errorf("%w: decoded length %d", ErrInvalidTicket, len(decoded))
	}
	if decoded[0] != 0x80 || (len(decoded)-57)%16 != 0 {
		return Ticket{}, fmt.Errorf("%w: shape", ErrInvalidTicket)
	}
	issued := time.Unix(int64(binary.BigEndian.Uint64(decoded[1:9])), 0)
	if issued.After(now.Add(FutureSkew)) {
		return Ticket{}, fmt.Errorf("%w: issued in future", ErrInvalidTicket)
	}
	expires := issued.Add(Lifetime)
	if !now.Before(expires) {
		return Ticket{}, fmt.Errorf("%w: expired", ErrInvalidTicket)
	}
	return Ticket{Raw: raw, IssuedAt: issued, ExpiresAt: expires, DecodedLength: len(decoded)}, nil
}

func (t Ticket) Valid(now time.Time) bool { return t.Raw != "" && now.Before(t.ExpiresAt) }
func (t Ticket) RenewalDue(now time.Time) bool {
	return t.Valid(now) && !now.Before(t.IssuedAt.Add(RenewAfter))
}

func CredentialHash(accessToken, accountID string) string {
	sum := sha256.Sum256([]byte(accessToken + "\x00" + accountID))
	return hex.EncodeToString(sum[:])
}
