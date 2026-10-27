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

type excelOAuthIdentity struct{ email, workspace, user string }

func consistentStringClaims(values ...string) (string, bool) {
	result := ""
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if result != "" && result != value {
			return "", false
		}
		result = value
	}
	return result, true
}
func excelCredentialIdentity(credentials map[string]any, allowExpired bool) (excelOAuthIdentity, error) {
	bad := func() (excelOAuthIdentity, error) {
		return excelOAuthIdentity{}, errors.New("Excel OAuth client, identity or expiry mismatch")
	}
	if stringClaim(credentials, "client_id") != ExcelClientID || stringClaim(credentials, "refresh_token") == "" {
		return bad()
	}
	access, e := tokenClaims(stringClaim(credentials, "access_token"))
	if e != nil {
		return bad()
	}
	client, consistent := consistentStringClaims(stringClaim(access, "client_id"), stringClaim(access, "azp"))
	if !consistent || client != ExcelClientID {
		return bad()
	}
	exp, _ := access["exp"].(float64)
	if exp <= 0 || (!allowExpired && exp <= float64(time.Now().Unix())) {
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
		for _, value := range aud {
			if value == ExcelClientID {
				audience = true
			}
		}
	}
	if !audience {
		return bad()
	}
	auth, _ := access["https://api.openai.com/auth"].(map[string]any)
	idAuth, _ := id["https://api.openai.com/auth"].(map[string]any)
	workspace := strings.TrimSpace(stringClaim(auth, "chatgpt_account_id"))
	user, consistent := consistentStringClaims(stringClaim(auth, "chatgpt_user_id"), stringClaim(auth, "user_id"))
	if !consistent || workspace == "" || user == "" {
		return bad()
	}
	email := strings.ToLower(strings.TrimSpace(stringClaim(id, "email")))
	if email == "" {
		return bad()
	}
	for _, value := range []string{stringClaim(idAuth, "chatgpt_account_id"), stringClaim(credentials, "chatgpt_account_id"), stringClaim(credentials, "workspace_id")} {
		if value != "" && strings.TrimSpace(value) != workspace {
			return bad()
		}
	}
	for _, value := range []string{stringClaim(idAuth, "chatgpt_user_id"), stringClaim(idAuth, "user_id"), stringClaim(credentials, "chatgpt_user_id"), stringClaim(credentials, "user_id")} {
		if value != "" && strings.TrimSpace(value) != user {
			return bad()
		}
	}
	accessProfile, _ := access["https://api.openai.com/profile"].(map[string]any)
	for _, value := range []string{stringClaim(accessProfile, "email"), stringClaim(access, "email"), stringClaim(credentials, "email")} {
		if value != "" && !strings.EqualFold(strings.TrimSpace(value), email) {
			return bad()
		}
	}
	return excelOAuthIdentity{email: email, workspace: workspace, user: user}, nil
}
func ValidateExcelCredentials(credentials map[string]any, expectedEmail, expectedWorkspace string) (string, string, error) {
	identity, e := excelCredentialIdentity(credentials, false)
	if e != nil {
		return "", "", e
	}
	if !strings.EqualFold(identity.email, strings.TrimSpace(expectedEmail)) || (expectedWorkspace != "" && identity.workspace != strings.TrimSpace(expectedWorkspace)) {
		return "", "", errors.New("Excel OAuth identity differs from configured account")
	}
	return identity.email, identity.workspace, nil
}

// Refresh responses may omit the ID token; callers retain the previous ID
// token before validating the complete merged grant against its original owner.
func ValidateExcelRefreshCredentials(next, previous map[string]any, email, workspace, user string) error {
	old, e := excelCredentialIdentity(previous, true)
	if e != nil {
		return e
	}
	refreshed, e := excelCredentialIdentity(next, false)
	if e != nil {
		return e
	}
	if refreshed != old {
		return errors.New("refreshed Excel authorization differs from its original owner")
	}
	if (email != "" && !strings.EqualFold(strings.TrimSpace(email), old.email)) || (workspace != "" && strings.TrimSpace(workspace) != old.workspace) || (user != "" && strings.TrimSpace(user) != old.user) {
		return errors.New("Excel grant owner differs from native account")
	}
	return nil
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
