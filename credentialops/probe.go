package credentialops

import "strings"

func ClassifyProbe(hasAccessToken bool, err error) string {
	if !hasAccessToken {
		return "auth"
	}
	if err == nil {
		return "ok"
	}
	message := strings.ToLower(err.Error())
	for _, marker := range []string{"status 401", "status=401", "invalid_token", "invalid token", "token expired", "invalid_grant", "requires re-login"} {
		if strings.Contains(message, marker) {
			return "auth"
		}
	}
	if strings.Contains(message, "unauthorized") && !strings.Contains(message, "403") {
		return "auth"
	}
	return "transient"
}
