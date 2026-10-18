package credentialops

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const RuntimeSourceCommit = "e46b0b00b00b37ab74d95f9d800cc6396d83f672"

type RuntimeManifest struct {
	SourceCommit string            `json:"source_commit"`
	Files        map[string]string `json:"files"`
}

func VerifyRuntimeBundle(root string) error {
	raw, e := os.ReadFile(filepath.Join(root, "manifest.json"))
	if e != nil {
		return errors.New("credential runtime manifest unavailable")
	}
	if expected := strings.ToLower(strings.TrimSpace(os.Getenv("CODEX2API_CREDENTIAL_OPS_MANIFEST_SHA256"))); expected != "" {
		sum := sha256.Sum256(raw)
		if subtle.ConstantTimeCompare([]byte(expected), []byte(hex.EncodeToString(sum[:]))) != 1 {
			return errors.New("credential runtime manifest checksum mismatch")
		}
	}
	var m RuntimeManifest
	if json.Unmarshal(raw, &m) != nil || m.SourceCommit != RuntimeSourceCommit || len(m.Files) == 0 {
		return errors.New("credential runtime source manifest mismatch")
	}
	for _, required := range []string{"python", "worker.py"} {
		if m.Files[required] == "" {
			return errors.New("credential runtime manifest is incomplete")
		}
	}
	for name, want := range m.Files {
		if name == "" || filepath.IsAbs(name) || filepath.Clean(name) != name || name == ".." || strings.HasPrefix(name, ".."+string(filepath.Separator)) {
			return errors.New("unsafe runtime manifest path")
		}
		path := filepath.Join(root, name)
		info, e := os.Lstat(path)
		if e != nil || !info.Mode().IsRegular() {
			return errors.New("credential runtime file unavailable")
		}
		resolved, e := filepath.EvalSymlinks(path)
		if e != nil {
			return e
		}
		absolute, e := filepath.Abs(root)
		if e != nil {
			return e
		}
		resolvedRoot, e := filepath.EvalSymlinks(absolute)
		if e != nil {
			return e
		}
		if !strings.HasPrefix(resolved, resolvedRoot+string(filepath.Separator)) {
			return errors.New("credential runtime file leaves bundle")
		}
		file, e := os.Open(path)
		if e != nil {
			return e
		}
		hash := sha256.New()
		_, e = io.Copy(hash, file)
		file.Close()
		if e != nil {
			return e
		}
		if !strings.EqualFold(want, hex.EncodeToString(hash.Sum(nil))) {
			return fmt.Errorf("credential runtime checksum mismatch: %s", name)
		}
	}
	return nil
}

var ownedWorkerToken struct {
	sync.RWMutex
	value string
}

func WorkerToken() string {
	if token := strings.TrimSpace(os.Getenv("CODEX2API_CREDENTIAL_OPS_WORKER_TOKEN")); len(token) >= 32 {
		return token
	}
	ownedWorkerToken.RLock()
	defer ownedWorkerToken.RUnlock()
	return ownedWorkerToken.value
}
func ensurePrivateWorkerToken(path string) (string, error) {
	if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		return "", e
	}
	if raw, e := os.ReadFile(path); e == nil {
		info, e := os.Lstat(path)
		if e != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || len(strings.TrimSpace(string(raw))) < 32 {
			return "", errors.New("worker token file is invalid or not private")
		}
		return strings.TrimSpace(string(raw)), nil
	}
	raw := make([]byte, 32)
	if _, e := rand.Read(raw); e != nil {
		return "", e
	}
	value := base64.RawURLEncoding.EncodeToString(raw)
	file, e := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		if os.IsExist(e) {
			return ensurePrivateWorkerToken(path)
		}
		return "", e
	}
	defer file.Close()
	if _, e = file.WriteString(value); e != nil {
		return "", e
	}
	if e = file.Sync(); e != nil {
		return "", e
	}
	dir, e := os.Open(filepath.Dir(path))
	if e != nil {
		return "", e
	}
	defer dir.Close()
	if e = dir.Sync(); e != nil {
		return "", e
	}
	return value, nil
}

type RuntimeSupervisor struct {
	Root, APIURL, StateDir string
	mu                     sync.Mutex
	done                   chan struct{}
	Status                 string
	Failure                string
	cmd                    *exec.Cmd
}

func NewRuntimeSupervisor(root, apiURL, stateDir string) *RuntimeSupervisor {
	return &RuntimeSupervisor{Root: root, APIURL: apiURL, StateDir: stateDir, Status: "stopped"}
}
func (r *RuntimeSupervisor) Start(ctx context.Context) error {
	if e := VerifyRuntimeBundle(r.Root); e != nil {
		return e
	}
	absolute, e := filepath.Abs(r.Root)
	if e != nil {
		return e
	}
	r.Root = absolute
	state, e := filepath.Abs(r.StateDir)
	if e != nil {
		return e
	}
	r.StateDir = state
	if e = os.MkdirAll(r.StateDir, 0700); e != nil {
		return e
	}
	if e = os.Chmod(r.StateDir, 0700); e != nil {
		return e
	}
	token := WorkerToken()
	if token == "" {
		token, e = ensurePrivateWorkerToken(filepath.Join(r.StateDir, "secrets", "credential-operations-worker.token"))
		if e != nil {
			return e
		}
		ownedWorkerToken.Lock()
		ownedWorkerToken.value = token
		ownedWorkerToken.Unlock()
	}
	r.mu.Lock()
	if r.done != nil {
		r.mu.Unlock()
		return errors.New("credential runtime already started")
	}
	r.done = make(chan struct{})
	r.Status = "starting"
	r.mu.Unlock()
	go func() {
		defer close(r.done)
		for ctx.Err() == nil {
			command := exec.Command(filepath.Join(r.Root, "python"), filepath.Join(r.Root, "worker.py"))
			configureRuntimeProcess(command)
			command.Dir = r.Root
			command.Env = []string{"PATH=" + r.Root + string(os.PathListSeparator) + os.Getenv("PATH"), "HOME=" + r.StateDir, "TOSUB2_ROOT=" + filepath.Join(r.Root, "tosub2"), "TURB_ROOT=" + filepath.Join(r.Root, "turb"), "TOSUB2_PYTHON=" + filepath.Join(r.Root, "python"), "NODE_EXECUTABLE=" + filepath.Join(r.Root, "node"), "PYTHONUNBUFFERED=1", "OPENAI_TOTP_JOURNAL_DIR=" + filepath.Join(r.StateDir, "credential-operations-recovery"), "CODEX2API_CREDENTIAL_OPS_URL=" + r.APIURL, "CODEX2API_CREDENTIAL_OPS_WORKER_TOKEN=" + token}
			r.mu.Lock()
			r.cmd = command
			r.mu.Unlock()
			if e := command.Start(); e != nil {
				r.setRuntimeStatus("failed", "credential runtime failed to start")
			} else {
				r.setRuntimeStatus("running", "")
				done := make(chan error, 1)
				go func() { done <- command.Wait() }()
				select {
				case <-ctx.Done():
					stopRuntimeProcess(command)
					<-done
					r.setRuntimeStatus("stopped", "")
					return
				case <-done:
					r.setRuntimeStatus("failed", "credential runtime exited; restart pending")
				}
			}
			select {
			case <-ctx.Done():
				r.setRuntimeStatus("stopped", "")
				return
			case <-time.After(3 * time.Second):
			}
		}
	}()
	return nil
}
func (r *RuntimeSupervisor) setRuntimeStatus(status, failure string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Status = status
	r.Failure = failure
}
func (r *RuntimeSupervisor) Snapshot() (string, string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.Status, r.Failure
}
func (r *RuntimeSupervisor) Wait() {
	r.mu.Lock()
	done := r.done
	r.mu.Unlock()
	if done != nil {
		<-done
	}
}
