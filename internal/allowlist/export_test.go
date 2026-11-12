package allowlist

import "strings"

// Rows renders the statement table rows for fields, so tests can check the
// renderer against an allowlist shape the real one does not have yet.
func Rows(fields []Field) string {
	var b strings.Builder
	writeRows(&b, "", fields)
	return b.String()
}
