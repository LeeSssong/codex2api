package siwc

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

func NormalizeRequest(body []byte) ([]byte, error) {
	var payload map[string]any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if decoder.Decode(&payload) != nil || payload == nil {
		return nil, errors.New("invalid Responses body")
	}
	if decoder.Decode(new(any)) != io.EOF {
		return nil, errors.New("invalid trailing Responses data")
	}
	if previous, ok := payload["previous_response_id"]; ok && previous != nil && previous != "" {
		return nil, errors.New("SIWC HTTP requires explicit conversation history")
	}
	for _, key := range []string{"background", "conversation", "previous_response_id", "max_output_tokens", "max_completion_tokens", "max_tool_calls", "metadata", "moderation", "multi_agent", "prompt", "prompt_cache_retention", "prompt_cache_options", "safety_identifier", "temperature", "top_logprobs", "top_p", "truncation", "user", "client_metadata", "type", "stream_id"} {
		delete(payload, key)
	}
	payload["store"], payload["stream"] = false, true
	if input, ok := payload["input"].(string); ok {
		payload["input"] = []any{map[string]any{"role": "user", "content": input, "type": "message"}}
	}
	if payload["input"] == nil {
		payload["input"] = []any{}
	}
	items, ok := payload["input"].([]any)
	if !ok {
		return nil, errors.New("invalid SIWC input")
	}
	kept := make([]any, 0, len(items))
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok {
			return nil, errors.New("invalid SIWC input item")
		}
		if item["role"] == "system" {
			item["role"] = "developer"
		}
		if item["type"] == "configuration_update" {
			if update, ok := item["reasoning"].(map[string]any); ok {
				if effort, ok := update["effort"].(string); ok {
					reasoning, _ := payload["reasoning"].(map[string]any)
					if reasoning == nil {
						reasoning = map[string]any{}
					}
					reasoning["effort"] = effort
					payload["reasoning"] = reasoning
				}
			}
			continue
		}
		kept = append(kept, item)
	}
	payload["input"] = kept
	if raw, present := payload["tools"]; present && raw != nil {
		tools, ok := raw.([]any)
		if !ok {
			return nil, errors.New("invalid SIWC tools")
		}
		grouped, local := []any{}, []any{}
		var functions map[string]any
		for _, raw := range tools {
			tool, ok := raw.(map[string]any)
			if !ok {
				return nil, errors.New("invalid SIWC tool")
			}
			switch tool["type"] {
			case "function", "custom":
				local = append(local, tool)
			case "web_search", "web_search_preview":
				grouped = append(grouped, tool)
			case "namespace":
				children, ok := tool["tools"].([]any)
				if !ok {
					return nil, errors.New("invalid SIWC tool namespace")
				}
				for _, child := range children {
					entry, ok := child.(map[string]any)
					if !ok || (entry["type"] != "function" && entry["type"] != "custom") {
						return nil, errors.New("SIWC namespace supports function and custom tools only")
					}
				}
				if tool["name"] == "functions" {
					if functions != nil {
						return nil, errors.New("duplicate functions namespace")
					}
					functions = tool
				}
				grouped = append(grouped, tool)
			default:
				return nil, errors.New("unsupported SIWC hosted tool; select Codex explicitly")
			}
		}
		if len(local) > 0 {
			if functions == nil {
				functions = map[string]any{"type": "namespace", "name": "functions", "description": "Client-provided tools.", "tools": []any{}}
				grouped = append(grouped, functions)
			}
			functions["tools"] = append(functions["tools"].([]any), local...)
		}
		payload["tools"] = grouped
	}
	return json.Marshal(payload)
}
