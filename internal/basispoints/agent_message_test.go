package basispoints

import (
	"bytes"
	"encoding/json"
	"io"
	"reflect"
	"strings"
	"testing"
)

func agentToolSource(name string) object {
	source := testSource()
	source["tools"] = []any{object{"type": "namespace", "name": "collaboration", "tools": []any{
		object{"type": "function", "name": name, "parameters": object{
			"type": "object", "properties": object{"message": object{"type": "string", "encrypted": true}},
		}},
	}}}
	return source
}

func assertAgentEncryption(t *testing.T, call object, want string) {
	t.Helper()
	raw, err := json.Marshal(call["encrypted_function_args"])
	if err != nil || string(raw) != want {
		t.Fatalf("encrypted_function_args = %s, want %s (error %v)", raw, want, err)
	}
}

func TestAgentMessagePlaintextAcrossStreamAndReplay(t *testing.T) {
	const task = "检查子代理消息\nKeep \"quotes\", tab\tand CRLF\r\nunchanged."
	for _, name := range []string{"spawn_agent", "send_message", "followup_task"} {
		for _, mode := range []string{"relay", "direct"} {
			t.Run(name+"/"+mode, func(t *testing.T) {
				cache := new(ReplayCache)
				source := agentToolSource(name)
				_, bridge := mustPrepare(t, source, "account/key/parent", cache)
				args := object{"message": task}
				if name == "spawn_agent" {
					args["task_name"], args["fork_turns"] = "worker", "none"
				} else {
					args["target"] = "/root/worker"
				}
				native := nativeCall(object{"name": "collaboration." + name, "arguments": args})
				if mode == "direct" {
					encoded, _ := json.Marshal(args)
					native["name"], native["arguments"] = "collaboration."+name, string(encoded)
				} else {
					// Wrapper metadata describes OfficeJS arguments, not the child task.
					native["encrypted_function_args"] = []any{"code"}
				}
				wire := sse(object{"type": "response.created", "response": object{"id": "resp_agent", "output": []any{}}}) +
					sse(object{"type": "response.output_item.done", "output_index": 0, "item": native}) +
					sse(object{"type": "response.completed", "response": object{"id": "resp_agent", "output": []any{native}}})
				stream := bridge.Stream(io.NopCloser(strings.NewReader(wire)))
				t.Cleanup(func() { _ = stream.Close() })
				output, err := io.ReadAll(stream)
				if err != nil {
					t.Fatal(err)
				}
				stages := make(map[string]int)
				var call object
				var delta, done string
				err = readEvents(bytes.NewReader(output), func(_ string, data []byte) error {
					var event object
					if err := decode(data, &event); err != nil {
						return err
					}
					kind := text(event["type"])
					switch kind {
					case "response.output_item.added", "response.output_item.done":
						item, _ := event["item"].(object)
						assertAgentEncryption(t, item, "[]")
						stages[kind]++
					case "response.function_call_arguments.delta":
						delta += text(event["delta"])
					case "response.function_call_arguments.done":
						done = text(event["arguments"])
					case "response.completed":
						response, _ := event["response"].(object)
						items, _ := response["output"].([]any)
						if len(items) != 1 {
							t.Fatalf("expected one client tool: %s", data)
						}
						call, _ = items[0].(object)
						assertAgentEncryption(t, call, "[]")
						stages[kind]++
					}
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
				for _, stage := range []string{"response.output_item.added", "response.output_item.done", "response.completed"} {
					if stages[stage] != 1 {
						t.Fatalf("%s emitted %d times", stage, stages[stage])
					}
				}
				if call["namespace"] != "collaboration" || call["name"] != name || delta != call["arguments"] || done != delta {
					t.Fatalf("tool identity or arguments changed: %+v", call)
				}
				var decoded object
				if err := decode([]byte(text(call["arguments"])), &decoded); err != nil || !reflect.DeepEqual(decoded, args) {
					t.Fatalf("task arguments changed: %+v (%v)", decoded, err)
				}

				// Model the client's plaintext child request without sharing parent cache state.
				childMessage := object{"type": "agent_message", "author": "/root", "recipient": "/root/worker",
					"content": []any{object{"type": "input_text", "text": decoded["message"]}}}
				child := testSource()
				child["input"] = []any{childMessage}
				prepared, _ := mustPrepare(t, child, "account/key/child", new(ReplayCache))
				items := prepared["input"].([]any)
				if !reflect.DeepEqual(items[len(items)-1], childMessage) {
					t.Fatal("child message changed")
				}

				source["input"] = []any{call, object{"type": "function_call_output", "call_id": call["call_id"], "output": "delivered"}}
				for _, replay := range []*ReplayCache{cache, new(ReplayCache)} {
					prepared, _ := mustPrepare(t, source, "account/key/parent", replay)
					items := prepared["input"].([]any)
					envelope := historyCollisionEnvelope(t, items[len(items)-2].(object))
					if !reflect.DeepEqual(envelope["arguments"], args) {
						t.Fatal("cache hit or history reconstruction changed the task")
					}
				}
			})
		}
	}
}

func TestAgentDirectEncryptionDeclaration(t *testing.T) {
	for _, tc := range []struct {
		name    string
		present bool
		value   any
		want    string
	}{
		{"missing", false, nil, "[]"},
		{"null", true, nil, "[]"},
		{"empty", true, []any{}, "[]"},
		{"encrypted", true, []any{"message"}, `["message"]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, bridge := mustPrepare(t, agentToolSource("spawn_agent"), "parent", nil)
			native := object{"type": "function_call", "name": "collaboration.spawn_agent", "call_id": "call_agent",
				"arguments": `{"message":"opaque-ciphertext","task_name":"worker"}`}
			if tc.present {
				native["encrypted_function_args"] = tc.value
			}
			call, err := bridge.translateCall(native)
			if err != nil {
				t.Fatal(err)
			}
			assertAgentEncryption(t, call, tc.want)
			if call["arguments"] != native["arguments"] {
				t.Fatal("direct encrypted argument content changed")
			}
		})
	}
}

func TestAgentUnknownCiphertextRemainsRejected(t *testing.T) {
	for _, content := range []string{"looks like a plaintext task", "gAAAAABopaqueCiphertext"} {
		source := testSource()
		source["input"] = []any{object{"type": "agent_message", "author": "/root", "recipient": "/root/worker",
			"content": []any{object{"type": "encrypted_content", "encrypted_content": content}}}}
		body, _ := json.Marshal(source)
		_, _, err := Prepare(body, "child", nil)
		if err == nil || !strings.Contains(err.Error(), "supports text and HTTPS input_image") {
			t.Fatalf("unknown ciphertext was accepted or silently discarded: %v", err)
		}
	}
}

func TestAgentEncryptionMetadataDoesNotChangeCustomTools(t *testing.T) {
	source := testSource()
	source["tools"] = []any{object{"type": "custom", "name": "apply_patch"}}
	_, bridge := mustPrepare(t, source, "scope", nil)
	native := object{"type": "custom_tool_call", "name": "apply_patch", "call_id": "call_patch",
		"input": "*** Begin Patch\n*** End Patch", "encrypted_function_args": []any{"message"}}
	call, err := bridge.translateCall(native)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := call["encrypted_function_args"]; exists || call["input"] != native["input"] {
		t.Fatal("function metadata leaked into a custom tool")
	}
}
