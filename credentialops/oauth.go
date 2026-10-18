package credentialops

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

const ExcelClientID = "app_fnr0pYvVwwFDocDumLG3H2Bp"

func tokenClaims(raw string) (map[string]any, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return nil, errors.New("invalid OAuth token")
	}
	data, e := base64.RawURLEncoding.DecodeString(parts[1])
	if e != nil {
		return nil, errors.New("invalid OAuth token")
	}
	var claims map[string]any
	e = json.Unmarshal(data, &claims)
	return claims, e
}
func stringClaim(v map[string]any, key string) string { s, _ := v[key].(string); return s }
func ValidateExcelCredentials(credentials map[string]any, expectedEmail, expectedWorkspace string) (string, string, error) {
	bad := func() (string, string, error) {
		return "", "", errors.New("Excel OAuth client, identity or expiry mismatch")
	}
	if stringClaim(credentials, "client_id") != ExcelClientID || stringClaim(credentials, "refresh_token") == "" {
		return bad()
	}
	access, e := tokenClaims(stringClaim(credentials, "access_token"))
	if e != nil {
		return bad()
	}
	client := stringClaim(access, "client_id")
	if client == "" {
		client = stringClaim(access, "azp")
	}
	if client != ExcelClientID {
		return bad()
	}
	exp, _ := access["exp"].(float64)
	if exp <= float64(time.Now().Unix()) {
		return bad()
	}
	id, e := tokenClaims(stringClaim(credentials, "id_token"))
	if e != nil {
		return bad()
	}
	audience := false
	switch aud := id["aud"].(type) {
	case string:
		audience = aud == ExcelClientID
	case []any:
		for _, a := range aud {
			if a == ExcelClientID {
				audience = true
			}
		}
	}
	if !audience {
		return bad()
	}
	email := strings.TrimSpace(stringClaim(id, "email"))
	auth, _ := access["https://api.openai.com/auth"].(map[string]any)
	idAuth, _ := id["https://api.openai.com/auth"].(map[string]any)
	workspace := stringClaim(auth, "chatgpt_account_id")
	user := stringClaim(auth, "chatgpt_user_id")
	if user == "" {
		user = stringClaim(auth, "user_id")
	}
	if email == "" || workspace == "" || user == "" || !strings.EqualFold(email, expectedEmail) || (expectedWorkspace != "" && workspace != expectedWorkspace) {
		return bad()
	}
	if declared := stringClaim(credentials, "chatgpt_account_id"); declared != "" && declared != workspace {
		return bad()
	}
	idUser := stringClaim(idAuth, "chatgpt_user_id")
	if idUser != "" && user != idUser {
		return bad()
	}
	return strings.ToLower(email), workspace, nil
}

func ValidateCodexClient(credentials map[string]any) error {
	const clientID = "app_EMoamEEZ73f0CkXaXp7hrann"
	invalid := errors.New("OAuth credential client does not match Codex profile")
	if declared := stringClaim(credentials, "client_id"); declared != "" && declared != clientID {
		return invalid
	}
	access, e := tokenClaims(stringClaim(credentials, "access_token"))
	if e != nil {
		return invalid
	}
	client := stringClaim(access, "client_id")
	if client == "" {
		client = stringClaim(access, "azp")
	}
	if client != clientID {
		return invalid
	}
	identity, e := tokenClaims(stringClaim(credentials, "id_token"))
	if e != nil {
		return invalid
	}
	switch aud := identity["aud"].(type) {
	case string:
		if aud == clientID {
			return nil
		}
	case []any:
		for _, value := range aud {
			if value == clientID {
				return nil
			}
		}
	}
	return invalid
}
