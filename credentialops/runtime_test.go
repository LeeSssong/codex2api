package credentialops

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCredentialOperationsRuntimeRejectsTamperedBundle(t *testing.T) {
	root := t.TempDir()
	data := []byte("#!/bin/sh\nexit 0\n")
	if e := os.WriteFile(filepath.Join(root, "python"), data, 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(root, "worker.py"), []byte("# owned worker"), 0600); e != nil {
		t.Fatal(e)
	}
	sum := sha256.Sum256(data)
	worker := sha256.Sum256([]byte("# owned worker"))
	manifest := RuntimeManifest{SourceCommit: "e46b0b00b00b37ab74d95f9d800cc6396d83f672", Files: map[string]string{"python": hex.EncodeToString(sum[:]), "worker.py": hex.EncodeToString(worker[:])}}
	raw, _ := json.Marshal(manifest)
	if e := os.WriteFile(filepath.Join(root, "manifest.json"), raw, 0600); e != nil {
		t.Fatal(e)
	}
	if e := VerifyRuntimeBundle(root); e != nil {
		t.Fatal(e)
	}
	os.WriteFile(filepath.Join(root, "worker.py"), []byte("tampered"), 0600)
	if e := VerifyRuntimeBundle(root); e == nil {
		t.Fatal("tampered runtime accepted")
	}
}
func TestCredentialOperationsRuntimeCancellationStopsOwnedProcess(t *testing.T) {
	root := t.TempDir()
	script := []byte("#!/bin/sh\nwhile true; do sleep 1; done\n")
	os.WriteFile(filepath.Join(root, "python"), script, 0700)
	os.WriteFile(filepath.Join(root, "worker.py"), []byte("#worker"), 0600)
	a := sha256.Sum256(script)
	b := sha256.Sum256([]byte("#worker"))
	raw, _ := json.Marshal(RuntimeManifest{SourceCommit: "e46b0b00b00b37ab74d95f9d800cc6396d83f672", Files: map[string]string{"python": hex.EncodeToString(a[:]), "worker.py": hex.EncodeToString(b[:])}})
	os.WriteFile(filepath.Join(root, "manifest.json"), raw, 0600)
	ctx, cancel := context.WithCancel(context.Background())
	runtime := NewRuntimeSupervisor(root, "http://127.0.0.1:18080/api/internal/credential-ops", t.TempDir())
	if e := runtime.Start(ctx); e != nil {
		t.Fatal(e)
	}
	cancel()
	done := make(chan struct{})
	go func() { runtime.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("owned runtime did not stop")
	}
}

func TestCredentialOperationsRuntimeMakesRelativeStatePrivateAndJournalAbsolute(t *testing.T) {
	t.Chdir(t.TempDir())
	root := t.TempDir()
	marker := filepath.Join(root, "observed-journal")
	script := []byte("#!/bin/sh\nprintf '%s' \"$OPENAI_TOTP_JOURNAL_DIR\" > \"" + marker + "\"\nwhile true; do sleep 1; done\n")
	if e := os.WriteFile(filepath.Join(root, "python"), script, 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(root, "worker.py"), []byte("#worker"), 0600); e != nil {
		t.Fatal(e)
	}
	a := sha256.Sum256(script)
	b := sha256.Sum256([]byte("#worker"))
	raw, _ := json.Marshal(RuntimeManifest{SourceCommit: RuntimeSourceCommit, Files: map[string]string{"python": hex.EncodeToString(a[:]), "worker.py": hex.EncodeToString(b[:])}})
	if e := os.WriteFile(filepath.Join(root, "manifest.json"), raw, 0600); e != nil {
		t.Fatal(e)
	}
	if e := os.Mkdir("state", 0755); e != nil {
		t.Fatal(e)
	}
	state, e := filepath.Abs("state")
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	runtime := NewRuntimeSupervisor(root, "http://127.0.0.1:18080/api/internal/credential-ops", "state")
	if e = runtime.Start(ctx); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { cancel(); runtime.Wait() })
	var observed []byte
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		observed, e = os.ReadFile(marker)
		if e == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	expected := filepath.Join(state, "credential-operations-recovery")
	if string(observed) != expected || !filepath.IsAbs(string(observed)) {
		t.Fatalf("child journal = %q, want absolute %q", observed, expected)
	}
	info, e := os.Stat(state)
	if e != nil || info.Mode().Perm() != 0700 {
		t.Fatalf("state is not private: %v %v", info, e)
	}
}
