package proxy

import (
	"crypto/sha256"
	"sync"
	"time"
)

// Basispoints selects a pool account per request and does not pin a conversation
// to one account, but reasoning encrypted_content is bound to the account that
// produced it. When a later turn's replayed ciphertext lands on a different
// account it fails with invalid_encrypted_content. The streaming handler already
// strips and retries that turn, but the client replays the same ciphertext every
// turn, so without memory each turn wastes an upstream attempt. This set records
// sessions whose ciphertext Basispoints has rejected so later turns strip it
// proactively. Only opaque session digests are kept, never request content, and
// entries expire so a session that settles on a stable account can use encrypted
// reasoning again.
const (
	basispointsEncryptedRejectTTL = 30 * time.Minute
	basispointsEncryptedRejectMax = 4096
)

type basispointsEncryptedRejectionSet struct {
	mu   sync.Mutex
	seen map[[sha256.Size]byte]time.Time
	now  func() time.Time
}

var basispointsEncryptedRejections = &basispointsEncryptedRejectionSet{seen: map[[sha256.Size]byte]time.Time{}, now: time.Now}

func (s *basispointsEncryptedRejectionSet) mark(session string) {
	if session == "" {
		return
	}
	key := sha256.Sum256([]byte(session))
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if _, exists := s.seen[key]; !exists && len(s.seen) >= basispointsEncryptedRejectMax {
		for digest, at := range s.seen {
			if now.Sub(at) > basispointsEncryptedRejectTTL {
				delete(s.seen, digest)
			}
		}
		for digest := range s.seen {
			if len(s.seen) < basispointsEncryptedRejectMax {
				break
			}
			delete(s.seen, digest)
		}
	}
	s.seen[key] = now
}

func (s *basispointsEncryptedRejectionSet) rejected(session string) bool {
	if session == "" {
		return false
	}
	key := sha256.Sum256([]byte(session))
	s.mu.Lock()
	defer s.mu.Unlock()
	at, ok := s.seen[key]
	if !ok {
		return false
	}
	if s.now().Sub(at) > basispointsEncryptedRejectTTL {
		delete(s.seen, key)
		return false
	}
	return true
}

// observeBasispointsEncryptedRejection wraps a Basispoints stream so a
// response.failed carrying invalid_encrypted_content marks the session. The
// observer only inspects bounded error envelopes and never retains content.
func observeBasispointsEncryptedRejection(body interface {
	Read([]byte) (int, error)
	Close() error
}, session string) *encryptedErrorObserver {
	return &encryptedErrorObserver{ReadCloser: body, stream: true, record: func(payload []byte) {
		if isRejectedEncryptedContentFailure(responseFailedErrorBody(payload)) {
			basispointsEncryptedRejections.mark(session)
		}
	}}
}
