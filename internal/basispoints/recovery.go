package basispoints

import (
	"encoding/json"
	"errors"
	"regexp"
	"strings"
)

// Recovery recognizes one complete catalog invocation. It never evaluates code,
// extracts envelopes from a program, or infers a tool from descriptive text.
var callShapedEnvelope = regexp.MustCompile(`^\s*(?:return\s+)?(?:await\s+)?([A-Za-z_][A-Za-z0-9_.\-]*)\s*\(`)

// catalogTool resolves a name the model used to a catalog entry, accepting the
// host's "functions." display prefix the same way direct calls do.
func (b *Bridge) catalogTool(name string) (string, tool, bool) {
	if info, ok := b.tools[name]; ok {
		return name, info, true
	}
	if trimmed := strings.TrimPrefix(name, "functions."); trimmed != name {
		if info, ok := b.tools[trimmed]; ok {
			return trimmed, info, true
		}
	}
	return "", tool{}, false
}

// recoverTransportEnvelope is consulted only after complete envelope decoding
// failed. Raw custom input must use the explicit custom transport marker.
func (b *Bridge) recoverTransportEnvelope(arguments object) (object, bool) {
	code, ok := arguments["code"].(string)
	if !ok || len(code) > maxEnvelopeBytes {
		return nil, false
	}
	return b.callShaped(code)
}

// callShaped uses the callee, never fields inside its argument, to select the
// client tool. A function takes one JSON object; a custom tool takes one string.
func (b *Bridge) callShaped(code string) (object, bool) {
	match := callShapedEnvelope.FindStringSubmatchIndex(code)
	if match == nil {
		return nil, false
	}
	key, info, known := b.catalogTool(code[match[2]:match[3]])
	if !known {
		return nil, false
	}
	rest := code[match[1]:]
	value, end, ok := decodeInvocationArgument(rest)
	if !ok && info.Kind == "function" {
		// Retain existing literal-string repairs for object arguments. These
		// repairs cannot supply missing delimiters or remove trailing code.
		rest = repairTransportJSONStrings(rest)
		value, end, ok = decodeInvocationArgument(rest)
	}
	if !ok {
		return nil, false
	}
	tail := strings.TrimSpace(rest[end:])
	if !strings.HasPrefix(tail, ")") {
		return nil, false
	}
	tail = strings.TrimSpace(tail[1:])
	if tail != "" && tail != ";" {
		return nil, false
	}
	switch info.Kind {
	case "function":
		if args, ok := value.(object); ok && args != nil {
			return object{"name": key, "arguments": args}, true
		}
	case "custom":
		if input, ok := value.(string); ok {
			return object{"name": key, "input": input}, true
		}
	}
	return nil, false
}

func decodeInvocationArgument(raw string) (any, int, bool) {
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, 0, false
	}
	return value, int(decoder.InputOffset()), true
}

// customInputText preserves the declared string contract without guessing which
// object property contains code or serializing an object into executable input.
func customInputText(value any) (string, error) {
	input, ok := value.(string)
	if !ok {
		return "", errors.New("Basispoints custom tool input must be a string")
	}
	return input, nil
}

// isNativeToolLeak reports whether a translation failure means the model called a
// tool that does not exist for this request, rather than misformatting a real one.
func isNativeToolLeak(err error) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	return strings.Contains(message, "unsupported native tool") || strings.Contains(message, "outside the client's catalog")
}
