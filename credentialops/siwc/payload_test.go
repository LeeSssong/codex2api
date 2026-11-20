package siwc

import (
	"encoding/json"
	"testing"
)

func TestNormalizeRequestPreservesConversationAndGroupsTools(t *testing.T) {
	raw, err := NormalizeRequest([]byte(`{"model":"gpt-5","input":[{"role":"system","content":"policy"},{"role":"user","content":"hello"},{"type":"configuration_update","reasoning":{"effort":"high"}}],"tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}}],"store":true,"temperature":0.1}`))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	input := got["input"].([]any)
	if len(input) != 2 || input[0].(map[string]any)["role"] != "developer" || got["store"] != false || got["stream"] != true || got["temperature"] != nil {
		t.Fatalf("unexpected normalization: %s", raw)
	}
	tools := got["tools"].([]any)
	if tools[0].(map[string]any)["type"] != "namespace" || got["reasoning"].(map[string]any)["effort"] != "high" {
		t.Fatalf("lost tools or reasoning: %s", raw)
	}
}

func TestNormalizeRequestRejectsUnsupportedHistoryAndTools(t *testing.T) {
	for _, raw := range []string{`{"previous_response_id":"resp_saved"}`, `{"tools":[{"type":"file_search"}]}`, `{"input":42}`, `{} {}`} {
		if _, err := NormalizeRequest([]byte(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}
