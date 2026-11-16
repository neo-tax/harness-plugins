package allowlist

import (
	"fmt"
	"strings"

	"github.com/neo-tax/harness-plugins/contract"
)

// enrollmentSection renders the statement's "At enrollment" section from the
// enrollment request in the wire contract, so it lists every field enrollment
// sends without a hand-kept copy.
//
// @spec: field-allowlist#statement-covers-enrollment
func enrollmentSection() string {
	var b strings.Builder
	b.WriteString(`
## At enrollment

Enrollment sends one request, when the engineer runs ` + "`neo-tax:login`" + `. That happens once per device, and again only if the device has to enroll again, for example after its device key is rejected. This request is separate from session events; its email and hostname are never sent with a session event. The list is generated from the enrollment request in the wire contract.

| Field | Contents |
| --- | --- |
`)
	for _, f := range contract.EnrollmentRequestFields() {
		fmt.Fprintf(&b, "| `%s` | %s |\n", f.Path, f.Description)
	}
	return b.String()
}
