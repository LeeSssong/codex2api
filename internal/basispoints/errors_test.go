package basispoints

import (
	"errors"
	"strings"
	"testing"
	"unicode"
)

func hasChinese(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Han, r) {
			return true
		}
	}
	return false
}

func TestPreparationFailuresExplainThemselvesInChinese(t *testing.T) {
	cases := []struct {
		body, category, hint string
	}{
		{`{"model":"gpt-6-astra","input":[{"type":"function_call_output","call_id":"missing","output":"x"}]}`, "tool_history", "新建会话"},
		{`{"model":"gpt-6-astra","input":[{"role":"user","content":[{"type":"input_image","image_url":"http://example.com/a.png"}]}]}`, "image_input", "HTTPS 图片"},
		{`{"model":"gpt-6-astra","input":"hi","tool_choice":"required"}`, "tool_choice", "auto 或 none"},
		{`{"model":"gpt-6-astra","input":"hi","tools":[{"type":"function"}]}`, "tool_catalog", "工具声明"},
		{`{"model":"gpt-6-astra","input":"hi","previous_response_id":"resp_1"}`, "history_reference", "完整的对话历史"},
		{`{"model":"gpt-6-astra","input":[{"type":"configuration_update"}]}`, "reasoning_configuration", "推理设置"},
		{`{"model":"gpt-6-astra","input":"hi","text":{"format":{"type":"json_schema"}}}`, "output_format", "结构化输出"},
		{`{"input":"hi"}`, "model", "model"},
		{`{"model":`, "request_json", "JSON"},
		{`{"model":"gpt-6-astra","input":5}`, "request_shape", "不支持的内容"},
	}
	for _, tc := range cases {
		_, _, err := Prepare([]byte(tc.body), "scope", new(ReplayCache))
		if err == nil {
			t.Fatalf("%s: expected a preparation failure", tc.category)
		}
		if got := Category(err); got != tc.category {
			t.Errorf("category for %s = %q", tc.body, got)
		}
		message := UserMessage(err)
		if !hasChinese(message) || !strings.Contains(message, tc.hint) || !strings.Contains(message, err.Error()) {
			t.Errorf("%s: user message lacks Chinese guidance or the English detail: %s", tc.category, message)
		}
	}
	if Category(nil) != "" || UserMessage(nil) != "" || ProtocolFailureMessage(nil) != "" {
		t.Fatal("nil errors must map to empty strings")
	}
}

func TestProtocolFailureMessageKeepsDiagnostics(t *testing.T) {
	cases := []struct {
		detail, hint string
	}{
		{"Basispoints returned an unsupported native tool; no tool was executed", "内置工具"},
		{"Basispoints returned a tool outside the client's catalog", "内置工具"},
		{"Basispoints tool transport code must contain one JSON client-tool envelope; OfficeJS and multiple calls are unsupported (format=text_or_code; bytes=12)", "格式约定"},
		{"Basispoints custom tools require input text, not arguments", "格式约定"},
		{"Basispoints completed response omitted an original tool item", "数据流不完整"},
		{"Basispoints SSE line exceeds 16 MiB", "数据流不完整"},
	}
	for _, tc := range cases {
		message := ProtocolFailureMessage(errors.New(tc.detail))
		if !hasChinese(message) || !strings.Contains(message, tc.hint) || !strings.Contains(message, tc.detail) {
			t.Errorf("%q → %q", tc.detail, message)
		}
	}
}
