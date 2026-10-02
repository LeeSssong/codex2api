package proxy

import (
	"github.com/codex2api/smartops"
	"github.com/tidwall/gjson"
	"testing"
)

func TestSmartOpsBPSBodyAppliesToolsAndCiphertextPolicy(t *testing.T) {
	body := []byte(`{"model":"gpt-6-astra","tools":[{"type":"web_search"},{"type":"function","name":"valid","parameters":{}}],"input":[{"type":"reasoning","encrypted_content":"secret"},{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}]}`)
	c := smartops.BPSDefaults{IgnoreEncryptedContent: true, OmitUnsupportedTools: true}
	got := prepareSmartOpsBPSBody(c, body)
	if gjson.GetBytes(got, "tools.#").Int() != 1 || gjson.GetBytes(got, "tools.0.name").String() != "valid" {
		t.Fatalf("tools=%s", got)
	}
	if gjson.GetBytes(got, "input.0.type").String() != "message" {
		t.Fatalf("ciphertext remains: %s", got)
	}
	unchanged := prepareSmartOpsBPSBody(smartops.BPSDefaults{}, body)
	if string(unchanged) != string(body) {
		t.Fatal("disabled flags altered request")
	}
}
