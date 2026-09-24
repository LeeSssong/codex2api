package basispoints

import (
	"fmt"
	"strings"
)

const maxEnvelopeBytes = 1 << 20

// decodeTransportCode unwraps common model formatting without evaluating code.
// Each layer must contain one complete JSON value; ambiguous batches are rejected.
func decodeTransportCode(value any) (object, error) {
	for depth := 0; depth < 4; depth++ {
		if item, ok := value.(object); ok && item != nil {
			return item, nil
		}
		raw, ok := value.(string)
		if !ok || len(raw) > maxEnvelopeBytes {
			break
		}
		raw = strings.TrimSpace(raw)
		if raw == "" {
			break
		}
		if decoded, ok := decodeEnvelopeValue(raw); ok {
			value = decoded
			continue
		}
		// Strip a complete Markdown fence, optionally preceded by a short prose label.
		if start := strings.Index(raw, "```"); start >= 0 && prosePrefix(raw[:start]) {
			fenced := raw[start:]
			newline := strings.IndexByte(fenced, '\n')
			if newline >= 0 && strings.HasSuffix(fenced, "```") {
				language := strings.TrimSpace(fenced[3:newline])
				if language == "" || strings.EqualFold(language, "json") {
					value = strings.TrimSpace(fenced[newline+1 : len(fenced)-3])
					continue
				}
			}
		}
		if start := strings.IndexByte(raw, '{'); start > 0 && prosePrefix(raw[:start]) {
			if decoded, ok := decodeEnvelopeValue(raw[start:]); ok {
				value = decoded
				continue
			}
		}
		break
	}
	return nil, fmt.Errorf("Basispoints tool transport code must contain one JSON client-tool envelope; OfficeJS and multiple calls are unsupported")
}

func prosePrefix(prefix string) bool {
	return len(prefix) <= 512 && !strings.ContainsAny(prefix, "{}[]();=`\"")
}

func decodeEnvelopeValue(raw string) (any, bool) {
	var value any
	if decode([]byte(raw), &value) == nil {
		return value, true
	}
	// Repair only illegal JSON escapes, and only after strict decoding fails.
	// Valid escapes, quotes and argument values otherwise retain their meaning.
	fixed := repairIllegalEscapes(raw)
	if fixed != raw && decode([]byte(fixed), &value) == nil {
		return value, true
	}
	return nil, false
}

func repairIllegalEscapes(raw string) string {
	var out strings.Builder
	out.Grow(len(raw))
	quoted := false
	for i := 0; i < len(raw); i++ {
		ch := raw[i]
		if ch == '"' {
			quoted = !quoted
		}
		if ch != '\\' || !quoted || i+1 >= len(raw) {
			out.WriteByte(ch)
			continue
		}
		next := raw[i+1]
		valid := strings.ContainsRune(`"\/bfnrt`, rune(next))
		if next == 'u' && i+5 < len(raw) {
			valid = true
			for _, digit := range raw[i+2 : i+6] {
				if !strings.ContainsRune("0123456789abcdefABCDEF", digit) {
					valid = false
				}
			}
		}
		out.WriteByte('\\')
		if valid {
			out.WriteByte(next)
			i++
		} else {
			out.WriteByte('\\')
		}
	}
	return out.String()
}
