package basispoints

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// These fixtures invoke only the protocol translator; no tool or network call runs.
func TestInvocationRecoveryContract(t *testing.T) {
	for _, tc := range []struct {
		name, code, wantName, wantArgs, wantInput string
		reject                                    bool
	}{
		{name: "json_envelope_large_integer", code: `{"name":"shell","arguments":{"number":9007199254740993}}`, wantName: "shell", wantArgs: `{"number":9007199254740993}`},
		{name: "simple_invocation_large_integer", code: `functions.shell({"number":9007199254740993})`, wantName: "shell", wantArgs: `{"number":9007199254740993}`},
		{name: "await_invocation", code: `await functions.shell({"command":"pwd"});`, wantName: "shell", wantArgs: `{"command":"pwd"}`},
		{name: "return_await_invocation", code: `return await functions.shell({"command":"pwd"});`, wantName: "shell", wantArgs: `{"command":"pwd"}`},
		{name: "spaced_semicolon", code: `functions.shell({"command":"pwd"} ) ; `, wantName: "shell", wantArgs: `{"command":"pwd"}`},
		{name: "custom_string_invocation", code: `functions.exec("text(\"hello\");\n");`, wantName: "exec", wantInput: "text(\"hello\");\n"},
		{name: "callee_not_argument_name", code: `functions.shell({"name":"exec","arguments":{"value":1}})`, wantName: "shell", wantArgs: `{"name":"exec","arguments":{"value":1}}`},
		{name: "reject_multiple_calls", code: `functions.shell({"name":"shell","arguments":{}}); functions.shell({})`, reject: true},
		{name: "reject_incomplete_call", code: `functions.shell({"name":"shell","arguments":{}}`, reject: true},
		{name: "reject_assignment", code: `const call = {"name":"shell","arguments":{}};`, reject: true},
		{name: "reject_unknown_wrapper", code: `unknown({"name":"shell","arguments":{}})`, reject: true},
		{name: "reject_multiple_arguments", code: `functions.shell({"name":"shell","arguments":{}}, {})`, reject: true},
		{name: "reject_custom_object", code: `{"name":"exec","input":{"code":"echo hello"}}`, reject: true},
		{name: "reject_executable_argument", code: `functions.shell({"command":other()})`, reject: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := testSource()
			source["tools"] = []any{object{"type": "function", "name": "shell"}, object{"type": "custom", "name": "exec"}}
			_, bridge := mustPrepare(t, source, "differential-review", new(ReplayCache))
			native := nativeCall(object{})
			native["arguments"] = object{"code": tc.code, "summary": "Call client tool"}
			got, err := bridge.translateCall(native)
			if tc.reject {
				if err == nil {
					t.Fatalf("expected rejection; got %#v", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got["name"] != tc.wantName {
				t.Fatalf("tool identity changed: want %s; got %#v", tc.wantName, got)
			}
			if tc.wantArgs != "" {
				var actual, expected object
				if err := decode([]byte(text(got["arguments"])), &actual); err != nil {
					t.Fatal(err)
				}
				if err := decode([]byte(tc.wantArgs), &expected); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(actual, expected) {
					t.Fatalf("arguments changed: want %#v; got %#v", expected, actual)
				}
			} else if got["input"] != tc.wantInput {
				t.Fatalf("custom input changed: %#v", got)
			}
		})
	}
}

func TestInvocationRecoveryPreservesReplayAndLiteralInput(t *testing.T) {
	for _, kind := range []string{"function", "custom"} {
		t.Run(kind, func(t *testing.T) {
			source := testSource()
			source["tools"] = []any{object{"type": "namespace", "name": "client", "tools": []any{object{"type": kind, "name": "run"}}}}
			cache := new(ReplayCache)
			_, bridge := mustPrepare(t, source, "account/key/thread", cache)
			input := "text(\"hello\");\r\n\tC:\\work\\file\n"
			literal := `{"name":"exec","number":9007199254740993}`
			if kind == "custom" {
				encoded, err := json.Marshal(input)
				if err != nil {
					t.Fatal(err)
				}
				literal = string(encoded)
			}
			native := transportCall("return await client.run("+literal+" ) ; ", "Call client tool")
			call, err := bridge.translateCall(native)
			if err != nil {
				t.Fatal(err)
			}
			if call["name"] != "run" || call["namespace"] != "client" {
				t.Fatalf("namespace or identity changed: %+v", call)
			}
			if kind == "custom" && call["input"] != input {
				t.Fatalf("literal custom input changed: %+v", call)
			}
			if kind == "function" {
				assertAgentEncryption(t, call, "[]")
				if !strings.Contains(text(call["arguments"]), "9007199254740993") {
					t.Fatal("argument lost numeric precision")
				}
			}
			if !reflect.DeepEqual(cache.get(bridge.scope, text(call["call_id"])), native) {
				t.Fatal("recovery changed the original native item")
			}
			source["input"] = []any{message("user", "run"), call, object{"type": text(call["type"]) + "_output", "call_id": call["call_id"], "output": "done"}}
			replayed, _ := mustPrepare(t, source, bridge.scope, cache)
			items := replayed["input"].([]any)
			if !reflect.DeepEqual(items[len(items)-2], native) || items[len(items)-1].(object)["output"] != "done" {
				t.Fatal("next turn did not retain original invocation and result")
			}
		})
	}
}

func TestRejectedInvocationsDoNotEmitPartialTools(t *testing.T) {
	for _, code := range []string{
		`functions.shell({"name":"shell","arguments":{"command":"private-command"}}); functions.shell({})`,
		`functions.shell({"name":"shell","arguments":{"command":"private-command"}}`,
		`const task = {"name":"shell","arguments":{"command":"private-command"}};`,
		`unknown({"name":"shell","arguments":{"command":"private-command"}})`,
		`functions.exec({"code":"private-command"})`,
	} {
		for _, summary := range []string{"Call client tool", "Run functions.exec"} {
			bridge, cache := recoveryBridge(t, object{"type": "function", "name": "shell"}, object{"type": "custom", "name": "exec"})
			valid := nativeCall(object{"name": "shell", "arguments": object{"command": "private-valid-command"}})
			invalid := transportCall(code, summary)
			wire := sse(object{"type": "response.output_item.done", "output_index": 0, "item": valid}) +
				sse(object{"type": "response.output_item.done", "output_index": 1, "item": invalid}) +
				sse(object{"type": "response.completed", "response": object{"output": []any{valid, invalid}}})
			output, _ := streamOutput(t, bridge, wire)
			got := string(output)
			if !strings.Contains(got, "response.failed") || !strings.Contains(got, "basispoints_protocol_error") {
				t.Fatalf("invalid invocation was not reported: %s", got)
			}
			for _, forbidden := range []string{"response.completed", "response.output_item.added", "response.function_call_arguments", "response.custom_tool_call_input", "private-"} {
				if strings.Contains(got, forbidden) {
					t.Fatalf("partial tool or private payload escaped: %s", got)
				}
			}
			if cache.get(bridge.scope, text(invalid["call_id"])) != nil {
				t.Fatal("invalid invocation entered replay history")
			}
		}
	}
}
