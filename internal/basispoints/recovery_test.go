package basispoints

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"strings"
	"testing"
)

func recoveryBridge(t *testing.T, tools ...object) (*Bridge, *ReplayCache) {
	t.Helper()
	cache := new(ReplayCache)
	source := testSource()
	source["tools"] = []any{}
	for _, tool := range tools {
		source["tools"] = append(source["tools"].([]any), tool)
	}
	_, bridge := mustPrepare(t, source, "scope", cache)
	return bridge, cache
}

func transportCall(code, summary string) object {
	arguments, _ := json.Marshal(object{"code": code, "summary": summary, "destructive": false, "references": []any{}})
	return object{"type": "function_call", "id": "fc_r", "call_id": "call_r", "name": "run_officejs", "arguments": string(arguments), "status": "completed"}
}

// Models sometimes wrap the JSON envelope in an assignment, prose or code. The
// envelope is recovered as data; nothing around it is evaluated.
func TestEmbeddedEnvelopeRecoveredFromCodeAndProse(t *testing.T) {
	bridge, cache := recoveryBridge(t, object{"type": "function", "name": "shell", "parameters": object{"type": "object"}})
	for name, code := range map[string]string{
		"assignment":     `const task = {"name":"shell","arguments":{"command":["ls","-la"]}};`,
		"return":         `return {"name":"shell","arguments":{"command":["ls","-la"]}}`,
		"office wrapper": `Excel.run(async (ctx) => { const call = {"name":"shell","arguments":{"command":["ls","-la"]}}; return call; });`,
		"prose with =":   `Running the listing now: envelope = {"name":"shell","arguments":{"command":["ls","-la"]}} (via transport)`,
		"host prefix":    `{"name":"functions.shell","arguments":{"command":["ls","-la"]}} // done`,
		"raw newline":    "const task = {\"name\":\"shell\",\"arguments\":{\"command\":[\"ls\",\"-la\"],\"note\":\"line1\nline2\"}};",
		"nested keys":    `call({"name":"shell","arguments":{"command":["ls","-la"],"env":{"name":"inner"}}})`,
	} {
		t.Run(name, func(t *testing.T) {
			call, err := bridge.translateCall(transportCall(code, "List files"))
			if err != nil {
				t.Fatalf("envelope not recovered: %v", err)
			}
			var args object
			if call["name"] != "shell" || call["type"] != "function_call" || decode([]byte(text(call["arguments"])), &args) != nil {
				t.Fatalf("wrong client call: %+v", call)
			}
			if command, _ := args["command"].([]any); len(command) != 2 || command[0] != "ls" {
				t.Fatalf("arguments changed: %+v", args)
			}
			if native := cache.get("scope", "call_r"); native == nil || !strings.Contains(text(native["arguments"]), "shell") {
				t.Fatal("original native item must replay verbatim")
			}
		})
	}
}

func TestEmbeddedEnvelopeRejectsAmbiguousUnknownOrBrokenContent(t *testing.T) {
	bridge, _ := recoveryBridge(t, object{"type": "function", "name": "shell", "parameters": object{"type": "object"}}, object{"type": "custom", "name": "apply_patch"})
	for name, code := range map[string]string{
		"two different calls": `first {"name":"shell","arguments":{"command":["ls"]}} then {"name":"shell","arguments":{"command":["pwd"]}}`,
		"unknown tool only":   `const task = {"name":"read_ranges","arguments":{"range":"A1"}};`,
		"truncated":           `const task = {"name":"shell","arguments":{"command":["ls"]`,
		"no envelope":         `await Excel.run(async (context) => { context.workbook.worksheets.getActiveWorksheet().getRange("A1").values = [[1]]; });`,
		"plain prose":         `I will list the files with the shell tool now.`,
		"function as raw":     `ls -la`,
	} {
		t.Run(name, func(t *testing.T) {
			if call, err := bridge.translateCall(transportCall(code, "Work on the task")); err == nil {
				t.Fatalf("ambiguous or unknown content dispatched: %+v", call)
			} else if strings.Contains(err.Error(), "read_ranges") || strings.Contains(err.Error(), "A1") {
				t.Fatalf("error leaked code content: %v", err)
			}
		})
	}
	// Identical repeated envelopes are one call, not an ambiguous batch.
	call, err := bridge.translateCall(transportCall(`{"name":"shell","arguments":{"command":["ls"]}} {"name":"shell","arguments":{"command":["ls"]}}`, "List"))
	if err != nil || call["name"] != "shell" {
		t.Fatalf("repeated identical envelope rejected: %v", err)
	}
}

func TestCallShapedEnvelopeRecoversFunctionCalls(t *testing.T) {
	bridge, _ := recoveryBridge(t, object{"type": "namespace", "name": "functions", "tools": []any{object{"type": "function", "name": "shell", "parameters": object{"type": "object"}}}}, object{"type": "custom", "name": "exec"})
	for _, code := range []string{
		`functions.shell({"command":["git","status"]})`,
		`await functions.shell({"command":["git","status"]});`,
		"return functions.shell({\"command\":[\"git\",\"status\"],\"pattern\":\"\\d+\"})",
	} {
		call, err := bridge.translateCall(transportCall(code, "Check status"))
		if err != nil || call["name"] != "shell" || call["namespace"] != "functions" {
			t.Fatalf("%s: call-shaped envelope not recovered: %+v, %v", code, call, err)
		}
		var args object
		if decode([]byte(text(call["arguments"])), &args) != nil || len(args["command"].([]any)) != 2 {
			t.Fatalf("%s: arguments lost: %+v", code, call["arguments"])
		}
	}
	// A bare name never resolves into a namespace, a custom tool has no JSON
	// arguments, and anything but one call expression is rejected.
	for _, code := range []string{
		`functions.shell({"command":["ls"]}); functions.shell({"command":["pwd"]})`,
		`exec({"code":"1+1"})`,
		`functions.shell(["ls"])`,
		`shell({"command":["ls"]})`,
		`read_ranges({"range":"A1"})`,
		`functions.shell({"command":["ls"]}) + other`,
	} {
		if call, err := bridge.translateCall(transportCall(code, "Work")); err == nil {
			t.Fatalf("%s: unsafe call shape accepted: %+v", code, call)
		}
	}
}

func TestRawCustomInputRecoveredWithoutMarker(t *testing.T) {
	bridge, _ := recoveryBridge(t, object{"type": "namespace", "name": "functions", "tools": []any{
		object{"type": "custom", "name": "apply_patch"}, object{"type": "custom", "name": "exec"}, object{"type": "function", "name": "shell", "parameters": object{"type": "object"}},
	}})
	patch := "*** Begin Patch\n*** Add File: hello.txt\n+hi\n*** End Patch\n"
	call, err := bridge.translateCall(transportCall(patch, "Create hello.txt"))
	if err != nil || call["type"] != "custom_tool_call" || call["name"] != "apply_patch" || call["namespace"] != "functions" || call["input"] != patch {
		t.Fatalf("patch body not routed to apply_patch: %+v, %v", call, err)
	}
	script := "const fs = require('fs');\nconsole.log(fs.readdirSync('.'));\n"
	for _, summary := range []string{"Run exec to list files", "functions.exec listing", "List files (exec)"} {
		call, err = bridge.translateCall(transportCall(script, summary))
		if err != nil || call["name"] != "exec" || call["input"] != script {
			t.Fatalf("%q: raw script not routed by summary: %+v, %v", summary, call, err)
		}
	}
	for name, native := range map[string]object{
		"summary names two custom tools": transportCall(script, "exec or apply_patch"),
		"summary names none":             transportCall(script, "List the files"),
		"summary names function tool":    transportCall(script, "Use shell"),
		"json-looking code":              transportCall(`{"command":["ls"] `, "Run exec"),
		"fenced code":                    transportCall("```js\n"+script+"```", "Run exec"),
		"execute as substring":           transportCall(script, "Execute the script"),
	} {
		t.Run(name, func(t *testing.T) {
			if call, err := bridge.translateCall(native); err == nil {
				t.Fatalf("raw input routed without a clear target: %+v", call)
			}
		})
	}
}

func TestCustomArgumentsAcceptedInsteadOfFailing(t *testing.T) {
	bridge, _ := recoveryBridge(t, object{"type": "custom", "name": "apply_patch"}, object{"type": "custom", "name": "exec"})
	for name, tc := range map[string]struct {
		envelope object
		want     string
	}{
		"arguments string":  {object{"name": "apply_patch", "arguments": "*** Begin Patch\n*** End Patch"}, "*** Begin Patch\n*** End Patch"},
		"arguments unwrap":  {object{"name": "apply_patch", "arguments": object{"patch": "*** Begin Patch\n*** End Patch"}}, "*** Begin Patch\n*** End Patch"},
		"args unwrap":       {object{"name": "exec", "args": object{"code": "if (a < b) { run(); }"}}, "if (a < b) { run(); }"},
		"input unwrap":      {object{"name": "exec", "input": object{"script": "1+1"}}, "1+1"},
		"multi key as json": {object{"name": "exec", "arguments": object{"code": "1<2", "timeout": json.Number("5")}}, `{"code":"1<2","timeout":5}`},
	} {
		t.Run(name, func(t *testing.T) {
			call, err := bridge.translateCall(nativeCall(tc.envelope))
			if err != nil || call["type"] != "custom_tool_call" || call["input"] != tc.want {
				t.Fatalf("custom arguments not accepted: %+v, %v", call, err)
			}
		})
	}
}

func TestDirectCallKindMismatchRecoveredWhenPayloadFits(t *testing.T) {
	bridge, _ := recoveryBridge(t, object{"type": "function", "name": "shell", "parameters": object{"type": "object"}}, object{"type": "custom", "name": "apply_patch"})
	patch := "*** Begin Patch\n*** End Patch"
	call, err := bridge.translateCall(object{"type": "function_call", "id": "1", "call_id": "c1", "name": "apply_patch", "arguments": `{"input":"` + strings.ReplaceAll(patch, "\n", `\n`) + `"}`})
	if err != nil || call["type"] != "custom_tool_call" || call["input"] != patch {
		t.Fatalf("custom tool called as function not recovered: %+v, %v", call, err)
	}
	call, err = bridge.translateCall(object{"type": "custom_tool_call", "id": "2", "call_id": "c2", "name": "shell", "input": `{"command":["ls"]}`})
	if err != nil || call["type"] != "function_call" || !strings.Contains(text(call["arguments"]), `"ls"`) {
		t.Fatalf("function tool called as custom not recovered: %+v, %v", call, err)
	}
	for _, native := range []object{
		{"type": "function_call", "id": "3", "call_id": "c3", "name": "apply_patch", "arguments": "{}"},
		{"type": "function_call", "id": "4", "call_id": "c4", "name": "apply_patch", "arguments": "not json"},
		{"type": "custom_tool_call", "id": "5", "call_id": "c5", "name": "shell", "input": "ls -la"},
		{"type": "custom_tool_call", "id": "6", "call_id": "c6", "name": "shell", "input": `["ls"]`},
	} {
		if call, err := bridge.translateCall(native); err == nil {
			t.Fatalf("payload without the declared kind's content dispatched: %+v", call)
		}
	}
}

func captureBridgeLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(previous) })
	return &buf
}

func streamOutput(t *testing.T, bridge *Bridge, wire string) ([]byte, []string) {
	t.Helper()
	body := bridge.Stream(io.NopCloser(strings.NewReader(wire)))
	out, err := io.ReadAll(body)
	_ = body.Close()
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	_ = readEvents(bytes.NewReader(out), func(_ string, data []byte) error {
		var event object
		if decode(data, &event) == nil {
			kinds = append(kinds, text(event["type"]))
		}
		return nil
	})
	return out, kinds
}

// A hallucinated host tool next to real assistant text is dropped so the client
// still receives the text; the native tool name is logged, nothing else.
func TestNativeToolLeakDroppedWhenTextAnswersTheTurn(t *testing.T) {
	logs := captureBridgeLogs(t)
	bridge, _ := recoveryBridge(t, object{"type": "function", "name": "shell", "parameters": object{"type": "object"}})
	message := object{"type": "message", "id": "msg_1", "role": "assistant", "status": "completed", "content": []any{object{"type": "output_text", "text": "Here is what I found in the workbook.", "annotations": []any{}}}}
	leak := object{"type": "function_call", "id": "fc_leak", "call_id": "call_leak", "name": "read_ranges", "arguments": `{"range":"PRIVATE_RANGE"}`, "status": "completed"}
	outside := nativeCall(object{"name": "search_workbook", "arguments": object{"query": "PRIVATE_QUERY"}})
	outside["id"], outside["call_id"] = "fc_out", "call_out"
	wire := sse(object{"type": "response.output_item.done", "output_index": 0, "item": message}) +
		sse(object{"type": "response.output_item.added", "output_index": 1, "item": leak}) +
		sse(object{"type": "response.output_item.done", "output_index": 1, "item": leak}) +
		sse(object{"type": "response.output_item.done", "output_index": 2, "item": outside}) +
		sse(object{"type": "response.completed", "response": object{"id": "resp_1", "status": "completed", "output": []any{message, leak, outside}}})
	out, kinds := streamOutput(t, bridge, wire)
	if bytes.Contains(out, []byte("response.failed")) || bytes.Contains(out, []byte("read_ranges")) || bytes.Contains(out, []byte("PRIVATE_")) || bytes.Contains(out, []byte("function_call")) {
		t.Fatalf("leaked tool reached the client or the turn failed: %s", out)
	}
	if !bytes.Contains(out, []byte("Here is what I found")) || kinds[len(kinds)-1] != "response.completed" {
		t.Fatalf("text was not delivered: %v", kinds)
	}
	var completed object
	_ = readEvents(bytes.NewReader(out), func(_ string, data []byte) error {
		var event object
		if decode(data, &event) == nil && text(event["type"]) == "response.completed" {
			completed = event["response"].(object)
		}
		return nil
	})
	if output, _ := completed["output"].([]any); len(output) != 1 {
		t.Fatalf("completed output must keep only the message: %+v", completed["output"])
	}
	if !strings.Contains(logs.String(), "dropped_native_tool count=2 tools=read_ranges,run_officejs") {
		t.Fatalf("dropped tool names not logged: %s", logs.String())
	}
	if strings.Contains(logs.String(), "PRIVATE_") {
		t.Fatal("tool arguments leaked into logs")
	}
}

func TestNativeToolLeakStillFailsWithoutTextOrBesideRealCalls(t *testing.T) {
	bridge, _ := recoveryBridge(t, object{"type": "function", "name": "shell", "parameters": object{"type": "object"}})
	leak := object{"type": "function_call", "id": "fc_leak", "call_id": "call_leak", "name": "read_ranges", "arguments": `{}`, "status": "completed"}
	valid := nativeCall(object{"name": "shell", "arguments": object{"command": []any{"ls"}}})
	message := object{"type": "message", "role": "assistant", "content": []any{object{"type": "output_text", "text": "Checking."}}}
	blank := object{"type": "message", "role": "assistant", "content": []any{object{"type": "output_text", "text": "   "}}}
	reasoning := object{"type": "reasoning", "id": "rs_1", "summary": []any{object{"type": "summary_text", "text": "thinking"}}}
	for name, output := range map[string][]any{
		"leak only":            {reasoning, leak},
		"leak with blank text": {blank, leak},
		"leak beside real":     {message, valid, leak},
	} {
		t.Run(name, func(t *testing.T) {
			wire := ""
			for i, item := range output {
				wire += sse(object{"type": "response.output_item.done", "output_index": i, "item": item})
			}
			wire += sse(object{"type": "response.completed", "response": object{"output": output}})
			out, _ := streamOutput(t, bridge, wire)
			if !bytes.Contains(out, []byte("response.failed")) || !bytes.Contains(out, []byte("basispoints_protocol_error")) || bytes.Contains(out, []byte("response.function_call_arguments")) {
				t.Fatalf("leak without a text answer must still fail without dispatching: %s", out)
			}
		})
	}
	// A malformed call to a real client tool is a bridge bug signal, not a leak.
	broken := transportCall(`I will run the command`, "Run")
	wire := sse(object{"type": "response.output_item.done", "output_index": 0, "item": message}) +
		sse(object{"type": "response.output_item.done", "output_index": 1, "item": broken}) +
		sse(object{"type": "response.completed", "response": object{"output": []any{message, broken}}})
	if out, _ := streamOutput(t, bridge, wire); !bytes.Contains(out, []byte("response.failed")) {
		t.Fatalf("malformed envelope must keep failing: %s", out)
	}
}

// When response.completed omits a tool item that already arrived complete in
// output_item.done, the done item is used instead of failing the turn.
func TestCompletedResponseRestoresToolItemsFromDoneEvents(t *testing.T) {
	logs := captureBridgeLogs(t)
	bridge, cache := recoveryBridge(t, object{"type": "function", "name": "shell", "parameters": object{"type": "object"}})
	first := nativeCall(object{"name": "shell", "arguments": object{"command": []any{"ls"}}})
	second := nativeCall(object{"name": "shell", "arguments": object{"command": []any{"pwd"}}})
	second["id"], second["call_id"] = "fc_second", "call_second"
	for name, completedOutput := range map[string][]any{
		"empty output":   {},
		"missing output": nil,
		"partial output": {first},
	} {
		t.Run(name, func(t *testing.T) {
			response := object{"id": "resp_1", "status": "completed"}
			if completedOutput != nil {
				response["output"] = completedOutput
			}
			wire := sse(object{"type": "response.output_item.done", "output_index": 0, "item": first}) +
				sse(object{"type": "response.output_item.done", "output_index": 1, "item": second}) +
				sse(object{"type": "response.completed", "response": response})
			out, kinds := streamOutput(t, bridge, wire)
			if bytes.Contains(out, []byte("response.failed")) || bytes.Contains(out, []byte("run_officejs")) {
				t.Fatalf("restored items failed or leaked: %s", out)
			}
			added := 0
			for _, kind := range kinds {
				if kind == "response.output_item.added" {
					added++
				}
			}
			if added != 2 || !bytes.Contains(out, []byte(`"call_id":"call_native"`)) || !bytes.Contains(out, []byte(`"call_id":"call_second"`)) {
				t.Fatalf("expected both client calls, got %d: %s", added, out)
			}
			if cache.get("scope", "call_second") == nil {
				t.Fatal("restored native item must be cached for replay")
			}
		})
	}
	if !strings.Contains(logs.String(), "restored_tool_items") {
		t.Fatalf("restoration not logged: %s", logs.String())
	}
	// A call the payload lists under another item ID is not dispatched twice.
	renamed := nativeCall(object{"name": "shell", "arguments": object{"command": []any{"ls"}}})
	renamed["id"] = "fc_renamed"
	wire := sse(object{"type": "response.output_item.done", "output_index": 0, "item": first}) +
		sse(object{"type": "response.completed", "response": object{"output": []any{renamed}}})
	out, kinds := streamOutput(t, bridge, wire)
	added := 0
	for _, kind := range kinds {
		if kind == "response.output_item.added" {
			added++
		}
	}
	if added != 1 || !bytes.Contains(out, []byte(`"id":"fc_renamed"`)) {
		t.Fatalf("renamed item dispatched %d times: %s", added, out)
	}
}

func TestProtocolHardeningNamesHostToolsInPrompt(t *testing.T) {
	for _, tools := range []any{nil, []any{object{"type": "function", "name": "shell", "parameters": object{"type": "object"}}}} {
		source := testSource()
		if tools != nil {
			source["tools"] = tools
		}
		wire, _ := mustPrepare(t, source, "scope", nil)
		raw, _ := json.Marshal(wire["input"])
		if !strings.Contains(string(raw), "read_ranges") || !strings.Contains(string(raw), "do not exist") {
			t.Fatalf("developer prompt must name host workbook tools as unavailable: %s", raw)
		}
	}
}
