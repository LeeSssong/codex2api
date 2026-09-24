package basispoints

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestTransportEnvelopeFormatting(t *testing.T) {
	const raw = `{"tool":"shell","args":{"cmd":"echo \"hello\"","n":9007199254740993}}`
	quoted, _ := json.Marshal(raw)
	var expected object
	if err := decode([]byte(raw), &expected); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]any{
		"plain": raw, "object": expected, "double encoded": string(quoted),
		"fenced":         "```json\n" + raw + "\n```",
		"labeled fence":  "Tool request:\n```json\n" + raw + "\n```",
		"labeled object": "Here is the tool request:\n" + raw,
	} {
		t.Run(name, func(t *testing.T) {
			got, err := decodeTransportCode(value)
			if err != nil || !reflect.DeepEqual(got, expected) {
				t.Fatalf("decoded envelope changed its arguments: %+v, %v", got, err)
			}
		})
	}
}

func TestTransportEnvelopeRepairsOnlyIllegalEscapes(t *testing.T) {
	got, err := decodeTransportCode(`{"name":"shell","arguments":{"pattern":"\d+\s","path":"C:\Projects\file","line":"a\nb","literal":"\\n"}}`)
	if err != nil {
		t.Fatal(err)
	}
	args := got["arguments"].(object)
	// The original valid \f escape retains its JSON meaning; guessing paths would alter arguments.
	if args["pattern"] != `\d+\s` || args["path"] != "C:\\Projects\file" || args["line"] != "a\nb" || args["literal"] != `\n` {
		t.Fatalf("escape repair changed valid content: %#v", args)
	}
}

func TestTransportEnvelopeRejectsAmbiguousOrExecutableContent(t *testing.T) {
	for _, raw := range []any{
		nil, 42, "", `[]`, `null`, `{"name":"shell"} {"name":"other"}`,
		`const task = {"name":"shell","arguments":{}};`,
		`Excel.run(async () => { return {"name":"shell","arguments":{}}; });`,
		"```js\n{\"name\":\"shell\"}\n```",
		"```json\n{\"name\":\"shell\"}\n```\n{\"name\":\"other\"}",
		`{"name":"shell","arguments":`, strings.Repeat("x", maxEnvelopeBytes+1),
	} {
		if got, err := decodeTransportCode(raw); err == nil {
			t.Fatalf("invalid envelope accepted: %+v", got)
		}
	}
}

func TestFormattedEnvelopeAndOutputOnlyReplay(t *testing.T) {
	cache := new(ReplayCache)
	source := testSource()
	source["tools"] = []any{object{"type": "function", "name": "get_weather"}}
	_, bridge := mustPrepare(t, source, "account/key", cache)
	native := nativeCall(object{})
	args := object{"code": "```json\n{\"tool\":\"get_weather\",\"args\":{\"city\":\"Tokyo\"}}\n```", "summary": "Weather", "references": []any{"Tokyo"}}
	encoded, _ := json.Marshal(args)
	native["arguments"] = string(encoded)
	call, err := bridge.translateCall(native)
	if err != nil {
		t.Fatal(err)
	}
	source["input"] = []any{message("user", "weather"), object{"type": "function_call_output", "call_id": call["call_id"], "output": "18 C"}}
	replayed, _ := mustPrepare(t, source, "account/key", cache)
	items := replayed["input"].([]any)
	if !reflect.DeepEqual(items[len(items)-2], native) {
		t.Fatal("formatting repair must not alter the original replay envelope")
	}
	output := items[len(items)-1].(object)
	if output["id"] != "fc_call_native" || output["call_id"] != call["call_id"] || output["output"] != "18 C" {
		t.Fatalf("incomplete tool output identity: %+v", output)
	}
}

func TestToolOutputIDsAreBoundedAndCallerIDsPreserved(t *testing.T) {
	for _, suppliedID := range []string{"", "caller_output_id"} {
		cache := new(ReplayCache)
		source := testSource()
		source["tools"] = []any{object{"type": "function", "name": "shell"}}
		_, bridge := mustPrepare(t, source, "account/key", cache)
		native := nativeCall(object{"name": "shell", "arguments": object{}})
		native["call_id"] = strings.Repeat("x", 100)
		call, err := bridge.translateCall(native)
		if err != nil {
			t.Fatal(err)
		}
		source["input"] = []any{message("user", "test"), call, object{"type": "function_call_output", "id": suppliedID, "call_id": call["call_id"], "output": "done"}}
		first, _ := mustPrepare(t, source, "account/key", cache)
		second, _ := mustPrepare(t, source, "account/key", cache)
		items := first["input"].([]any)
		id := text(items[len(items)-1].(object)["id"])
		if id == "" || len(id) > 64 || (suppliedID != "" && id != suppliedID) || !reflect.DeepEqual(first, second) {
			t.Fatal("tool output ID must be bounded, stable and preserve supplied IDs")
		}
	}
}
