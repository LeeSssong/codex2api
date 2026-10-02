package admin

import (
	"encoding/json"
	"net/http"

	"github.com/codex2api/smartops"
)

// SmartOpsConfigResponse is the stable admin transport shape. Root routing may
// embed these handlers without changing the existing Handler contract.
type SmartOpsConfigResponse struct {
	OAuth    smartops.OAuthAutoConfig `json:"oauth_auto_config"`
	Priority smartops.PriorityConfig  `json:"priority_scheduling"`
	Plugins  map[string]bool          `json:"plugins"`
}

func WriteSmartOpsConfig(w http.ResponseWriter, response SmartOpsConfigResponse) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(response)
}

func DecodeOAuthAutoConfig(r *http.Request) (smartops.OAuthAutoConfig, error) {
	var c smartops.OAuthAutoConfig
	err := json.NewDecoder(r.Body).Decode(&c)
	if err == nil {
		err = smartops.ValidateOAuthAutoConfig(c)
	}
	return c, err
}
func DecodePriorityConfig(r *http.Request) (smartops.PriorityConfig, error) {
	var c smartops.PriorityConfig
	err := json.NewDecoder(r.Body).Decode(&c)
	if err == nil {
		err = smartops.ValidatePriorityConfig(c)
	}
	return c, err
}
