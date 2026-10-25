package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const maxLog = 1 << 20

// Logf appends one line to agent.log in the plugin data directory, rotating
// it at 1 MB. Callers pass only non-content values: counts, hook names,
// harness versions, error classes. Never a path, payload, key, or email.
func Logf(dataDir, format string, args ...any) {
	if dataDir == "" {
		return
	}
	path := filepath.Join(dataDir, "agent.log")
	if fi, err := os.Stat(path); err == nil && fi.Size() > maxLog {
		os.Rename(path, path+".1")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s %s\n", time.Now().UTC().Format(time.RFC3339), fmt.Sprintf(format, args...))
}
