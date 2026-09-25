package accountops

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

func ParseJudgment(output string) (*Judgment, error) {
	if len(output) > 8000 {
		return nil, fmt.Errorf("judge response too large")
	}
	var result struct {
		Verdict string `json:"verdict"`
		Reason  string `json:"reason"`
	}

	decoder := json.NewDecoder(strings.NewReader(strings.TrimSpace(output)))
	first, err := decoder.Token()
	if err != nil || first != json.Delim('{') {
		return nil, fmt.Errorf("expected judge object")
	}
	seen := map[string]bool{}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		key, ok := token.(string)
		if !ok || seen[key] {
			return nil, fmt.Errorf("duplicate judge field")
		}
		seen[key] = true
		switch key {
		case "verdict":
			err = decoder.Decode(&result.Verdict)
		case "reason":
			err = decoder.Decode(&result.Reason)
		default:
			return nil, fmt.Errorf("unknown judge field")
		}
		if err != nil {
			return nil, err
		}
	}
	if _, err = decoder.Token(); err != nil {
		return nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("extra judge response")
	}

	if result.Verdict != "correct" && result.Verdict != "incorrect" && result.Verdict != "unknown" {
		return nil, fmt.Errorf("invalid verdict")
	}
	if strings.TrimSpace(result.Reason) == "" || len(result.Reason) > 2000 {
		return nil, fmt.Errorf("missing or oversized reason")
	}
	return &Judgment{Verdict: result.Verdict, Reason: result.Reason}, nil
}
