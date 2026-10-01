package ipv6state

import (
	"encoding/json"
	"io"
	"net/http"
)

// Only an explicit state rejection warrants discarding an unexpired value.
// Authentication, quota, proxy and transport failures retain it for replay.
func replayRejected(response *http.Response) bool {
	if response == nil || response.Body == nil || response.StatusCode != http.StatusBadRequest && response.StatusCode != http.StatusUnprocessableEntity {
		return false
	}
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&body) != nil {
		return false
	}
	switch body.Error.Code {
	case "invalid_turn_state", "turn_state_invalid", "turn_state_expired", "expired_turn_state":
		return true
	default:
		return false
	}
}
