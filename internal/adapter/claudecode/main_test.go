package claudecode_test

import (
	"os"
	"testing"
	"time"

	"github.com/neo-tax/harness-plugins/internal/adapter/claudecode"
)

// Most fixtures have no cost-state line, so SessionEnd would wait the full
// bound for one on every test; tests that exercise the wait set their own.
func TestMain(m *testing.M) {
	claudecode.CostStateWait = 50 * time.Millisecond
	os.Exit(m.Run())
}
