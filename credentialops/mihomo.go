package credentialops

// Adapted from the Sub2API credential-login transient node lease: each login
// owns one immutable node/listener. No shared selector or Sub2API daemon is used.
import (
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

const mihomoVersion = "v1.19.31"

type MihomoInput struct {
	Nodes       string `json:"nodes"`
	Binary      string `json:"binary"`
	Concurrency int    `json:"concurrency"`
}
type mihomoConfig struct {
	Nodes       []map[string]any
	Binary      string
	Concurrency int
}
type MihomoView struct {
	Configured  bool     `json:"configured"`
	Installed   bool     `json:"installed"`
	NodeCount   int      `json:"node_count"`
	Nodes       []string `json:"nodes"`
	Active      int      `json:"active"`
	Concurrency int      `json:"concurrency"`
	Binary      string   `json:"binary"`
	Version     string   `json:"version"`
}

var mihomoState = struct {
	sync.Mutex
	configs  map[string]mihomoConfig
	active   map[string]int
	next     map[string]int
	children map[string]context.CancelFunc
}{configs: map[string]mihomoConfig{}, active: map[string]int{}, next: map[string]int{}, children: map[string]context.CancelFunc{}}

func mihomoDir() string {
	return filepath.Clean(filepath.Join(filepath.Dir(KeyFile()), "..", "credential-mihomo"))
}
func privateFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".private-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err = f.Chmod(0600); err != nil {
		return err
	}
	if _, err = f.Write(data); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	d, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
func loadMihomoLocked() (mihomoConfig, error) {
	dir := mihomoDir()
	if c, ok := mihomoState.configs[dir]; ok {
		return c, nil
	}
	raw, err := os.ReadFile(filepath.Join(dir, "config.sealed"))
	if err != nil {
		return mihomoConfig{}, err
	}
	_, dec := EnvCrypto()
	plain, err := dec(string(raw))
	if err != nil {
		return mihomoConfig{}, err
	}
	var c mihomoConfig
	if err = json.Unmarshal([]byte(plain), &c); err != nil {
		return c, err
	}
	mihomoState.configs[dir] = c
	return c, nil
}
func SaveMihomoConfig(ctx context.Context, in MihomoInput) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if in.Concurrency < 1 || in.Concurrency > 16 {
		return errors.New("Mihomo concurrency must be 1–16")
	}
	mihomoState.Lock()
	defer mihomoState.Unlock()
	dir := mihomoDir()
	if mihomoState.active[dir] > 0 {
		return errors.New("wait for active node leases to finish before changing the pool")
	}
	old, _ := loadMihomoLocked()
	c := old
	c.Concurrency = in.Concurrency
	if in.Binary != "" {
		if !filepath.IsAbs(in.Binary) {
			return errors.New("Mihomo binary path must be absolute")
		}
		info, e := os.Stat(in.Binary)
		if e != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
			return errors.New("Mihomo binary is not executable")
		}
		c.Binary = in.Binary
	}
	if c.Binary == "" {
		c.Binary = filepath.Join(dir, "mihomo")
	}
	if strings.TrimSpace(in.Nodes) != "" {
		if len(in.Nodes) > 4<<20 {
			return errors.New("node configuration too large")
		}
		var document struct {
			Proxies []map[string]any `yaml:"proxies"`
		}
		if err := yaml.Unmarshal([]byte(in.Nodes), &document); err != nil {
			return errors.New("nodes must be a Clash YAML or JSON proxies document")
		}
		if len(document.Proxies) == 0 || len(document.Proxies) > 256 {
			return errors.New("provide 1–256 proxy nodes")
		}
		names := map[string]bool{}
		for _, node := range document.Proxies {
			name, _ := node["name"].(string)
			kind, _ := node["type"].(string)
			server, _ := node["server"].(string)
			if name == "" || len(name) > 200 || names[name] || server == "" || strings.ContainsAny(server, "/\r\n") {
				return errors.New("invalid or duplicate proxy node")
			}
			switch kind {
			case "socks5", "http", "ss", "vmess", "vless", "trojan", "hysteria2", "tuic":
			default:
				return errors.New("unsupported proxy node type")
			}
			if err := validateNodeValue(node); err != nil {
				return err
			}
			names[name] = true
		}
		c.Nodes = document.Proxies
	}
	if len(c.Nodes) == 0 {
		return errors.New("provide proxy nodes before enabling the pool")
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return err
	}
	enc, _ := EnvCrypto()
	sealed, err := enc(string(raw))
	if err != nil {
		return err
	}
	if err = privateFile(filepath.Join(dir, "config.sealed"), []byte(sealed)); err != nil {
		return err
	}
	mihomoState.configs[dir] = c
	return nil
}
func validateNodeValue(value any) error {
	switch x := value.(type) {
	case map[string]any:
		for key, v := range x {
			lower := strings.ToLower(key)
			if strings.Contains(lower, "certificate") || strings.Contains(lower, "private-key") || lower == "ca" || lower == "ca-cert" || lower == "plugin" || lower == "dialer-proxy" || lower == "proxy-provider" {
				return errors.New("file paths, plugins and external node providers are not accepted")
			}
			if err := validateNodeValue(v); err != nil {
				return err
			}
		}
	case []any:
		for _, v := range x {
			if err := validateNodeValue(v); err != nil {
				return err
			}
		}
	}
	return nil
}
func MihomoStatus() MihomoView {
	mihomoState.Lock()
	defer mihomoState.Unlock()
	c, err := loadMihomoLocked()
	v := MihomoView{Configured: err == nil, Concurrency: c.Concurrency, Binary: c.Binary, Version: mihomoVersion, Nodes: []string{}, Active: mihomoState.active[mihomoDir()]}
	if v.Concurrency == 0 {
		v.Concurrency = 3
	}
	if v.Binary == "" {
		v.Binary = filepath.Join(mihomoDir(), "mihomo")
	}
	info, e := os.Stat(v.Binary)
	v.Installed = e == nil && info.Mode().IsRegular() && info.Mode().Perm()&0111 != 0
	for _, node := range c.Nodes {
		name, _ := node["name"].(string)
		v.Nodes = append(v.Nodes, name)
	}
	v.NodeCount = len(v.Nodes)
	return v
}
func AcquireMihomo(ctx context.Context, scope string) (string, func(), error) {
	if err := ctx.Err(); err != nil {
		return "", nil, err
	}
	if scope == "" {
		return "", nil, errors.New("login task identity required")
	}
	mihomoState.Lock()
	c, err := loadMihomoLocked()
	dir := mihomoDir()
	if err != nil || len(c.Nodes) == 0 {
		mihomoState.Unlock()
		return "", nil, errors.New("managed node pool is not configured")
	}
	if mihomoState.active[dir] >= c.Concurrency {
		mihomoState.Unlock()
		return "", nil, errors.New("managed node pool is busy")
	}
	index := mihomoState.next[dir] % len(c.Nodes)
	mihomoState.next[dir]++
	mihomoState.active[dir]++
	mihomoState.Unlock()
	var once sync.Once
	releaseCount := func() { once.Do(func() { mihomoState.Lock(); mihomoState.active[dir]--; mihomoState.Unlock() }) }
	if err = os.MkdirAll(dir, 0700); err != nil {
		releaseCount()
		return "", nil, err
	}
	jobDir, err := os.MkdirTemp(dir, "lease-*")
	if err != nil {
		releaseCount()
		return "", nil, err
	}
	fail := func(err error) (string, func(), error) { os.RemoveAll(jobDir); releaseCount(); return "", nil, err }
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fail(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	data, _ := json.Marshal(map[string]any{"mixed-port": port, "allow-lan": false, "bind-address": "127.0.0.1", "mode": "rule", "log-level": "silent", "proxies": []map[string]any{c.Nodes[index]}, "rules": []string{"MATCH," + c.Nodes[index]["name"].(string)}, "dns": map[string]any{"enable": false}})
	config := filepath.Join(jobDir, "config.json")
	if err = privateFile(config, data); err != nil {
		return fail(err)
	}
	// A lost external Worker cannot retain a local node forever. This exceeds
	// the login executor's 25-minute deadline and stops the actual proxy process.
	life, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	cmd := exec.CommandContext(life, c.Binary, "-d", jobDir, "-f", config)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C.UTF-8"}
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err = cmd.Start(); err != nil {
		cancel()
		return fail(errors.New("managed Mihomo process could not start; install its pinned binary"))
	}
	nonce := make([]byte, 8)
	rand.Read(nonce)
	key := scope + hex.EncodeToString(nonce)
	done := make(chan struct{})
	mihomoState.Lock()
	mihomoState.children[key] = cancel
	mihomoState.Unlock()
	go func() {
		defer close(done)
		_ = cmd.Wait()
		os.RemoveAll(jobDir)
		releaseCount()
		mihomoState.Lock()
		delete(mihomoState.children, key)
		mihomoState.Unlock()
	}()
	release := func() { cancel(); <-done }
	deadline := time.NewTimer(8 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			release()
			return "", nil, ctx.Err()
		case <-done:
			return "", nil, errors.New("managed Mihomo exited before readiness")
		case <-deadline.C:
			release()
			return "", nil, errors.New("managed Mihomo did not become ready")
		case <-tick.C:
			conn, e := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 100*time.Millisecond)
			if e == nil {
				conn.Close()
				return fmt.Sprintf("http://127.0.0.1:%d", port), release, nil
			}
		}
	}
}
func CloseMihomo() {
	mihomoState.Lock()
	cancels := make([]context.CancelFunc, 0, len(mihomoState.children))
	for _, cancel := range mihomoState.children {
		cancels = append(cancels, cancel)
	}
	mihomoState.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
}

// InstallMihomo accepts only the pinned owner's release asset and its published
// sha256 digest. Binary provenance is checked before publishing an executable.
func InstallMihomo(ctx context.Context) error {
	if runtime.GOOS != "linux" || (runtime.GOARCH != "arm64" && runtime.GOARCH != "amd64") {
		return errors.New("automatic Mihomo installation supports Linux amd64/arm64; configure an installed binary on this platform")
	}
	name := "mihomo-linux-" + runtime.GOARCH + "-" + mihomoVersion + ".gz"
	if runtime.GOARCH == "amd64" {
		name = "mihomo-linux-amd64-compatible-" + mihomoVersion + ".gz"
	}
	client := &http.Client{Timeout: 3 * time.Minute}
	request, err := http.NewRequestWithContext(ctx, "GET", "https://api.github.com/repos/MetaCubeX/mihomo/releases/tags/"+mihomoVersion, nil)
	if err != nil {
		return err
	}
	request.Header.Set("User-Agent", "Codex2API")
	response, err := client.Do(request)
	if err != nil {
		return errors.New("could not read pinned Mihomo release")
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return errors.New("pinned Mihomo release unavailable")
	}
	var release struct {
		Assets []struct {
			Name   string `json:"name"`
			Digest string `json:"digest"`
			URL    string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err = json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(&release); err != nil {
		return err
	}
	address, digest := "", ""
	for _, asset := range release.Assets {
		if asset.Name == name {
			address = asset.URL
			digest = strings.TrimPrefix(asset.Digest, "sha256:")
		}
	}
	expected := "https://github.com/MetaCubeX/mihomo/releases/download/" + mihomoVersion + "/" + name
	if address != expected || len(digest) != 64 {
		return errors.New("pinned Mihomo asset has no verifiable release digest")
	}
	request, _ = http.NewRequestWithContext(ctx, "GET", address, nil)
	response, err = client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return errors.New("Mihomo binary download failed")
	}
	compressed, err := io.ReadAll(io.LimitReader(response.Body, 64<<20))
	if err != nil {
		return err
	}
	actual := sha256.Sum256(compressed)
	if hex.EncodeToString(actual[:]) != digest {
		return errors.New("Mihomo release checksum mismatch")
	}
	reader, err := gzip.NewReader(strings.NewReader(string(compressed)))
	if err != nil {
		return err
	}
	defer reader.Close()
	binary, err := io.ReadAll(io.LimitReader(reader, 192<<20))
	if err != nil || len(binary) >= 192<<20 {
		return errors.New("Mihomo binary is incomplete or too large")
	}
	target := filepath.Join(mihomoDir(), "mihomo")
	if err = privateFile(target, binary); err != nil {
		return err
	}
	return os.Chmod(target, 0700)
}
