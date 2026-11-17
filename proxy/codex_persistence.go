package proxy

import (
	"os"
	"strings"
)

// Match the service's mounted state directory while retaining local execution.
func codexPersistentStateDir() string {
	if dir := strings.TrimSpace(os.Getenv("DATA_DIR")); dir != "" {
		return dir
	}
	if info, err := os.Stat("/data"); err == nil && info.IsDir() {
		return "/data"
	}
	return "data"
}
