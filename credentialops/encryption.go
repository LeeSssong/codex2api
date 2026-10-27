package credentialops

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
)

var keyInitMu sync.Mutex

func KeyFile() string {
	if p := os.Getenv("CODEX2API_CREDENTIAL_OPS_KEY_FILE"); p != "" {
		return p
	}
	root := os.Getenv("DATA_DIR")
	if root == "" {
		root = "data"
	}
	return filepath.Join(root, "secrets", "credential-operations.key")
}
func EncryptionStatus() (bool, string) {
	if os.Getenv("CODEX2API_CREDENTIAL_OPS_KEY") != "" {
		enc, _ := EnvCrypto()
		_, e := enc("check")
		return e == nil, "environment"
	}
	if _, e := os.Stat(KeyFile()); e == nil {
		enc, _ := EnvCrypto()
		_, e := enc("check")
		return e == nil, "file"
	}
	return false, "unconfigured"
}
func InitializeEncryption(hasCiphertext bool) (bool, string, error) {
	keyInitMu.Lock()
	defer keyInitMu.Unlock()
	if ok, source := EncryptionStatus(); ok {
		return true, source, nil
	}
	if os.Getenv("CODEX2API_CREDENTIAL_OPS_KEY") != "" {
		return false, "environment", errors.New("credential encryption environment key must be base64 of 32 random bytes")
	}
	nodeCipher := filepath.Join(filepath.Dir(KeyFile()), "..", "credential-mihomo", "config.sealed")
	if _, e := os.Lstat(nodeCipher); e == nil {
		hasCiphertext = true
	} else if !os.IsNotExist(e) {
		return false, "missing", e
	}
	if hasCiphertext {
		return false, "missing", errors.New("encryption key is missing while encrypted data exists; restore the original key")
	}
	path := KeyFile()
	if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		return false, "", e
	}
	if e := os.Chmod(filepath.Dir(path), 0700); e != nil {
		return false, "", e
	}
	key := make([]byte, 32)
	if _, e := io.ReadFull(rand.Reader, key); e != nil {
		return false, "", e
	}
	temp, e := os.CreateTemp(filepath.Dir(path), ".credential-key-*")
	if e != nil {
		return false, "", e
	}
	defer os.Remove(temp.Name())
	defer temp.Close()
	if e = temp.Chmod(0600); e != nil {
		return false, "", e
	}
	if _, e = temp.WriteString(base64.StdEncoding.EncodeToString(key)); e != nil {
		return false, "", e
	}
	if e = temp.Sync(); e != nil {
		return false, "", e
	}
	if e = temp.Close(); e != nil {
		return false, "", e
	}
	if e = os.Link(temp.Name(), path); e != nil {
		if ok, src := EncryptionStatus(); ok {
			return true, src, nil
		}
		return false, "", e
	}
	dir, e := os.Open(filepath.Dir(path))
	if e != nil {
		return false, "", e
	}
	defer dir.Close()
	if e = dir.Sync(); e != nil {
		return false, "", e
	}
	return true, "file", nil
}
