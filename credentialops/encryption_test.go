package credentialops

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCredentialOperationsEncryptionInitializeIsPrivateAndRefusesMissingKey(t *testing.T) {
	t.Setenv("CODEX2API_CREDENTIAL_OPS_KEY", "")
	path := filepath.Join(t.TempDir(), "secrets", "key")
	t.Setenv("CODEX2API_CREDENTIAL_OPS_KEY_FILE", path)
	if _, _, e := InitializeEncryption(true); e == nil {
		t.Fatal("replaced missing key")
	}
	if ok, _, e := InitializeEncryption(false); e != nil || !ok {
		t.Fatal(e)
	}
	info, e := os.Stat(path)
	if e != nil || info.Mode().Perm() != 0600 {
		t.Fatal("key permissions")
	}
	enc, dec := EnvCrypto()
	cipher, e := enc("private")
	if e != nil {
		t.Fatal(e)
	}
	if got, e := dec(cipher); e != nil || got != "private" {
		t.Fatal(e)
	}
	before, _ := os.ReadFile(path)
	if _, _, e := InitializeEncryption(false); e != nil {
		t.Fatal(e)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("key overwritten")
	}
}

func TestCredentialOperationsEncryptionRefusesOrphanedMihomoCiphertext(t *testing.T) {
	t.Setenv("CODEX2API_CREDENTIAL_OPS_KEY", "")
	key := filepath.Join(t.TempDir(), "secrets", "credential-operations.key")
	t.Setenv("CODEX2API_CREDENTIAL_OPS_KEY_FILE", key)
	sealed := filepath.Join(filepath.Dir(key), "..", "credential-mihomo", "config.sealed")
	if e := os.MkdirAll(filepath.Dir(sealed), 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(sealed, []byte("synthetic-encrypted-node-config"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, _, e := InitializeEncryption(false); e == nil {
		t.Fatal("replaced missing key while encrypted node configuration exists")
	}
	if _, e := os.Stat(key); !os.IsNotExist(e) {
		t.Fatal("missing node key was regenerated", e)
	}
}
