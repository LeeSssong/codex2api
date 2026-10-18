package credentialops

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestMihomoConfigurationIsEncryptedAndPublicViewIsRedacted(t *testing.T) {
	t.Setenv("DATA_DIR", t.TempDir())
	t.Setenv("CODEX2API_CREDENTIAL_OPS_KEY", "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	err := SaveMihomoConfig(context.Background(), MihomoInput{Nodes: "proxies:\n  - name: node-one\n    type: socks5\n    server: proxy.example.test\n    port: 1080\n    username: operator\n    password: private-node-password\n", Concurrency: 3})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(KeyFile()), "..", "credential-mihomo", "config.sealed"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "private-node-password") {
		t.Fatal("node secret persisted in plaintext")
	}
	status := MihomoStatus()
	if status.NodeCount != 1 || status.Nodes[0] != "node-one" {
		t.Fatalf("status: %#v", status)
	}
}

func TestMihomoLeasePinsProcessAndReleasesCapacity(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is needed for the local process fixture")
	}
	t.Setenv("DATA_DIR", t.TempDir())
	t.Setenv("CODEX2API_CREDENTIAL_OPS_KEY", "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	binary := filepath.Join(t.TempDir(), "mihomo-fixture")
	script := "#!" + python + "\nimport sys,json,socket\np=sys.argv[sys.argv.index('-f')+1]\nc=json.load(open(p))\ns=socket.socket();s.bind(('127.0.0.1',c['mixed-port']));s.listen()\nwhile True:\n c,a=s.accept();c.close()\n"
	if err = os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	if err = SaveMihomoConfig(context.Background(), MihomoInput{Binary: binary, Nodes: `{"proxies":[{"name":"lease-node","type":"socks5","server":"example.test","port":1080}]}`, Concurrency: 1}); err != nil {
		t.Fatal(err)
	}
	proxy, release, err := AcquireMihomo(context.Background(), "task:1")
	if err != nil {
		t.Fatal(err)
	}
	defer CloseMihomo()
	if !strings.HasPrefix(proxy, "http://127.0.0.1:") || MihomoStatus().Active != 1 {
		t.Fatal("lease was not pinned to an independent local listener")
	}
	if _, _, err = AcquireMihomo(context.Background(), "task:2"); err == nil {
		t.Fatal("concurrency capacity was ignored")
	}
	release()
	if MihomoStatus().Active != 0 {
		t.Fatal("node lease remained active after child exit")
	}
}

func TestMihomoRejectsProviderAndFileBasedNodes(t *testing.T) {
	t.Setenv("DATA_DIR", t.TempDir())
	t.Setenv("CODEX2API_CREDENTIAL_OPS_KEY", "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	if err := SaveMihomoConfig(context.Background(), MihomoInput{Nodes: `{"proxies":[{"name":"bad","type":"socks5","server":"example.test","port":1080,"certificate":"/private/key"}]}`, Concurrency: 1}); err == nil {
		t.Fatal("filesystem access passed node validation")
	}
}

func TestMihomoFailsClosedWhenBinaryOrNodesUnavailable(t *testing.T) {
	t.Setenv("DATA_DIR", t.TempDir())
	CloseMihomo()
	_, _, err := AcquireMihomo(context.Background(), "test")
	if err == nil {
		t.Fatal("missing node pool silently fell back to direct")
	}
}
