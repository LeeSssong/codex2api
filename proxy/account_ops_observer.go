package proxy

import (
	"github.com/codex2api/accountops"
	"github.com/codex2api/auth"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
)

var accountOpsObserver atomic.Pointer[accountops.AccountOpsService]

func SetAccountOpsObserver(s *accountops.AccountOpsService) { accountOpsObserver.Store(s) }

type accountOpsBody struct {
	io.ReadCloser
	data    []byte
	once    sync.Once
	observe func([]byte)
}

func (b *accountOpsBody) Read(p []byte) (int, error) {
	n, e := b.ReadCloser.Read(p)
	if len(b.data) <= 32768 {
		remaining := 32769 - len(b.data)
		b.data = append(b.data, p[:min(n, remaining)]...)
	}
	if e == io.EOF {
		b.once.Do(func() { b.observe(b.data) })
	}
	return n, e
}
func (b *accountOpsBody) Close() error {
	b.once.Do(func() { b.observe(b.data) })
	return b.ReadCloser.Close()
}

// Collect only while the existing caller reads errors. Never read ahead, persist
// raw failures, block on DB/SMTP, or change status/header/body semantics.
func observeAccountOpsResponse(a *auth.Account, r *http.Response) {
	s := accountOpsObserver.Load()
	if s == nil || a == nil || r == nil || r.StatusCode < 400 || r.StatusCode > 599 {
		return
	}
	if _, ok := r.Body.(*accountOpsBody); ok {
		return
	}
	platform := accountops.PlatformOpenAI
	if a.IsClaudeOAuth() {
		platform = accountops.PlatformAnthropic
	} else if a.IsGrokAPI() {
		platform = "grok"
	} else if a.IsAntigravityAPI() {
		platform = "google"
	}
	a.Mu().RLock()
	account := accountops.Account{ID: a.DBID, Name: a.Email, Platform: platform}
	a.Mu().RUnlock()
	status, headers := r.StatusCode, r.Header.Clone()
	observe := func(body []byte) { s.Observe(&account, status, headers, body) }
	if r.Body == nil {
		observe(nil)
		return
	}
	r.Body = &accountOpsBody{ReadCloser: r.Body, observe: observe}
}

func observeBasispointsOps(accountID, generation int64, category string) {
	if s := accountOpsObserver.Load(); s != nil {
		s.ObserveBasispoints(accountID, generation, category)
	}
}
