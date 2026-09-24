package basispoints

import (
	"strings"
	"testing"
)

func TestNativeCodexReasonRoutesOnlyExplicitCapabilities(t *testing.T) {
	image := `{"type":"input_image","image_url":"data:image/png;base64,AAAA"}`
	cases := []struct {
		name, body, want string
	}{
		{"plain text", `{"model":"gpt-6-astra","input":"hi"}`, ""},
		{"client tools", `{"model":"gpt-6-astra","input":"hi","tools":[{"type":"function","name":"shell","parameters":{"type":"object"}}]}`, ""},
		{"codex default cached web search", `{"model":"gpt-6-astra","input":"hi","tools":[{"type":"web_search","external_web_access":false}]}`, ""},
		{"codex live web search", `{"model":"gpt-6-astra","input":"hi","tools":[{"type":"web_search","external_web_access":true}]}`, RouteWebSearch},
		{"codex indexed web search", `{"model":"gpt-6-astra","input":"hi","tools":[{"type":"web_search","external_web_access":true,"indexed_web_access":true}]}`, RouteWebSearch},
		{"bare web search declaration", `{"model":"gpt-6-astra","input":"hi","tools":[{"type":"web_search_preview"}]}`, RouteWebSearch},
		{"web search inside namespace", `{"model":"gpt-6-astra","input":"hi","tools":[{"type":"namespace","name":"functions","tools":[{"type":"web_search"}]}]}`, RouteWebSearch},
		{"web search in additional_tools", `{"model":"gpt-6-astra","input":[{"type":"additional_tools","tools":[{"type":"web_search"}]},{"role":"user","content":"hi"}]}`, RouteWebSearch},
		{"forced web search", `{"model":"gpt-6-astra","input":"hi","tool_choice":{"type":"web_search"}}`, RouteWebSearch},
		{"image generation tool", `{"model":"gpt-6-astra","input":"hi","tools":[{"type":"image_generation"}]}`, RouteImageGeneration},
		{"forced image generation", `{"model":"gpt-6-astra","input":"hi","tool_choice":{"type":"image_generation"}}`, RouteImageGeneration},
		{"forced function", `{"model":"gpt-6-astra","input":"hi","tool_choice":{"type":"function","name":"shell"}}`, RouteToolChoice},
		{"required tool choice", `{"model":"gpt-6-astra","input":"hi","tool_choice":"required"}`, RouteToolChoice},
		{"auto tool choice", `{"model":"gpt-6-astra","input":"hi","tool_choice":"auto"}`, ""},
		{"https image stays", `{"model":"gpt-6-astra","input":[{"type":"message","role":"user","content":[{"type":"input_image","image_url":"https://example.com/a.png","detail":"auto"}]}]}`, ""},
		{"data image in message", `{"model":"gpt-6-astra","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"look"},` + image + `]}]}`, RouteImageInput},
		{"file id image", `{"model":"gpt-6-astra","input":[{"type":"message","role":"user","content":[{"type":"input_image","file_id":"file_1"}]}]}`, RouteImageInput},
		{"data image in tool output", `{"model":"gpt-6-astra","input":[{"type":"function_call_output","call_id":"c1","output":[` + image + `]}]}`, RouteImageInput},
		{"string tool output stays", `{"model":"gpt-6-astra","input":[{"type":"function_call_output","call_id":"c1","output":"ok"}]}`, ""},
		{"input file", `{"model":"gpt-6-astra","input":[{"type":"message","role":"user","content":[{"type":"input_file","file_data":"x"}]}]}`, RouteInputContent},
		{"json schema", `{"model":"gpt-6-astra","input":"hi","text":{"format":{"type":"json_schema","name":"a","schema":{}}}}`, RouteOutputFormat},
		{"chat response_format", `{"model":"gpt-6-astra","input":"hi","response_format":{"type":"json_object"}}`, RouteOutputFormat},
		{"text format stays", `{"model":"gpt-6-astra","input":"hi","text":{"format":{"type":"text"}}}`, ""},
		{"invalid json", `{"model":`, ""},
	}
	for _, tc := range cases {
		if got := NativeCodexReason([]byte(tc.body)); got != tc.want {
			t.Errorf("%s: reason=%q want %q", tc.name, got, tc.want)
		}
	}
}

func TestBridgeHintsHowToEnableWebSearch(t *testing.T) {
	body, bridge, err := Prepare([]byte(`{"model":"gpt-6-astra","input":"hi","tools":[{"type":"function","name":"shell","parameters":{"type":"object"}},{"type":"web_search","external_web_access":false}]}`), "scope", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(bridge.Warnings) != 1 || !strings.Contains(string(body), "--search") || !strings.Contains(string(body), "web search is off on this channel") {
		t.Fatalf("cached web search declaration lacks the opt-in hint: warnings=%v body=%s", bridge.Warnings, body)
	}
	body, _, err = Prepare([]byte(`{"model":"gpt-6-astra","input":"hi","tools":[{"type":"function","name":"shell","parameters":{"type":"object"}},{"type":"image_generation"}]}`), "scope", nil)
	if err != nil || strings.Contains(string(body), "--search") {
		t.Fatalf("hint must only follow an omitted web search tool: err=%v", err)
	}
}
