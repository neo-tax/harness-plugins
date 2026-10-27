// Package enroll implements device enrollment: the one exchange in which a
// device presents an enrollment token and a confirmed email and receives its
// device key, and the storage of that key in the plugin data directory.
package enroll

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/mail"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/neo-tax/harness-plugins/contract"
	"github.com/neo-tax/harness-plugins/internal/ingest"
	"github.com/neo-tax/harness-plugins/internal/queue"
)

// DeviceFile holds the device id and key inside the plugin data directory.
const DeviceFile = "device.json"

// DefaultServer is Neo.Tax's enrollment endpoint base URL.
const DefaultServer = "https://app.neo.tax"

var (
	// ErrNotEnrolled means no device key is stored.
	ErrNotEnrolled = errors.New("this device is not enrolled; run neo-tax:login")
	// ErrNeedInput means the token or email is missing and the agent could
	// collect it neither on a terminal nor in a browser form; the command
	// to run in a terminal was written to Out.
	ErrNeedInput = errors.New("enrollment needs an enrollment token and an email")
)

// Device is what a device holds after enrollment.
type Device struct {
	DeviceID      string    `json:"device_id"`
	DeviceKey     string    `json:"device_key"`
	IngestBaseURL string    `json:"ingest_base_url"`
	Email         string    `json:"email"`
	EnrolledAt    time.Time `json:"enrolled_at"`
}

// Load reads the stored device.
func Load(dataDir string) (Device, error) {
	var d Device
	b, err := os.ReadFile(filepath.Join(dataDir, DeviceFile))
	if errors.Is(err, os.ErrNotExist) {
		return d, ErrNotEnrolled
	}
	if err != nil {
		return d, err
	}
	if err := json.Unmarshal(b, &d); err != nil || d.DeviceKey == "" {
		return Device{}, ErrNotEnrolled
	}
	return d, nil
}

// Client returns an ingest client authorized with the stored device key.
func Client(dataDir string, hc *http.Client) (*ingest.Client, error) {
	d, err := Load(dataDir)
	if err != nil {
		return nil, err
	}
	return &ingest.Client{HTTP: hc, BaseURL: d.IngestBaseURL, DeviceKey: d.DeviceKey, DeviceID: d.DeviceID}, nil
}

// save writes the device file readable only by the owning user, replacing
// any previous key.
//
// @spec: device-enrollment#key-storage-location
// @spec: device-enrollment#one-key-per-device
func save(dataDir string, d Device) error {
	// The harness creates the plugin data directory; only a directory this
	// call creates is given owner-only permissions. The key file itself is
	// always owner-only.
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(dataDir, DeviceFile+".tmp")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dataDir, DeviceFile))
}

// Options configures one login.
type Options struct {
	DataDir        string
	Server         string // enrollment base URL; DefaultServer when empty
	Token          string
	Email          string
	Harness        string
	HarnessVersion string
	AgentVersion   string
	// AccountEmail returns the email in the harness's own account file, or
	// "". It is read only here, during login, never on the hook path.
	AccountEmail func() string
	// Interactive prompts on Out and reads answers from In.
	Interactive bool
	In          io.Reader
	// ReadSecret, when set, reads the token without echoing it.
	ReadSecret func() (string, error)
	// Browser, when not Interactive, collects missing input in a form served
	// on the loopback interface, opened with OpenURL, for at most Wait
	// (BrowserWait when zero).
	Browser bool
	OpenURL func(url string) error
	Wait    time.Duration
	// TerminalCommand is the login command an engineer can run in a
	// terminal instead; it is named whenever input is collected elsewhere.
	TerminalCommand string
	Out             io.Writer
	HTTP            *http.Client
}

// Login enrolls this device. Missing input is prompted for on a terminal,
// or collected in a browser form; the token is never written to Out, and
// with neither, Login names the terminal command and returns ErrNeedInput.
//
// @spec: device-enrollment#interactive-enrollment
// @spec: device-enrollment#enrollment-is-the-only-way-to-get-a-key
// @spec: device-enrollment#token-never-in-conversation
func Login(ctx context.Context, o Options) error {
	if o.Out == nil {
		o.Out = io.Discard
	}
	if o.TerminalCommand == "" {
		o.TerminalCommand = "neo-tax-agent login"
	}
	token, email := strings.TrimSpace(o.Token), strings.TrimSpace(o.Email)

	if token == "" || email == "" {
		candidates := emailCandidates(ctx, o.AccountEmail)
		if !o.Interactive {
			if o.Browser {
				return browserLogin(ctx, o, candidates, email)
			}
			fmt.Fprintf(o.Out, "Enrolling needs your Neo.Tax enrollment token, which should not go through this conversation. Run this in a terminal:\n  %s\n", o.TerminalCommand)
			return ErrNeedInput
		}
		in := bufio.NewReader(o.In)
		if token == "" {
			fmt.Fprint(o.Out, "Paste the enrollment token from your Neo.Tax admin: ")
			if o.ReadSecret != nil {
				t, err := o.ReadSecret()
				fmt.Fprintln(o.Out)
				if err != nil {
					return err
				}
				token = strings.TrimSpace(t)
			} else {
				token = readLine(in)
			}
		}
		if email == "" {
			email = promptEmail(o.Out, in, candidates)
		}
	}
	return exchange(ctx, o, token, email)
}

// exchange checks the input, enrolls with the server, and stores the key.
// Its errors are written for the engineer and never carry the token.
func exchange(ctx context.Context, o Options, token, email string) error {
	if token == "" {
		return errors.New("no enrollment token given")
	}
	if _, err := mail.ParseAddress(email); err != nil || strings.ContainsAny(email, " <>") {
		return fmt.Errorf("%q is not an email address", email)
	}

	server := o.Server
	if server == "" {
		server = DefaultServer
	}
	host, _ := os.Hostname()
	var hv *string
	if o.HarnessVersion != "" {
		hv = &o.HarnessVersion
	}
	// @spec: device-enrollment#email-confirmed-by-engineer
	resp, err := ingest.Enroll(ctx, o.HTTP, server, contract.EnrollmentRequest{
		ContractVersion:  contract.Version,
		EnrollmentToken:  token,
		Email:            email,
		EmailConfirmedBy: contract.EmailConfirmedByEngineer,
		Device: contract.DeviceDescription{
			Hostname: host, OS: runtime.GOOS, Arch: runtime.GOARCH,
			Harness: o.Harness, HarnessVersion: hv, AgentVersion: o.AgentVersion,
		},
	})
	var se *ingest.StatusError
	if errors.As(err, &se) && se.Code == http.StatusUnauthorized {
		return errors.New("the enrollment token was rejected; it may have expired or been revoked. Ask your Neo.Tax admin for a new one")
	}
	if err != nil {
		return fmt.Errorf("enrollment failed: %w", err)
	}
	if err := save(o.DataDir, Device{
		DeviceID: resp.DeviceID, DeviceKey: resp.DeviceKey, IngestBaseURL: resp.IngestBaseURL,
		Email: email, EnrolledAt: time.Now().UTC(),
	}); err != nil {
		return err
	}
	// @spec: device-enrollment#rejected-key-stops-flushing
	if err := queue.New(o.DataDir).Enrolled(); err != nil {
		return err
	}
	fmt.Fprintf(o.Out, "Enrolled this device as %s.\n", email)
	return nil
}

// emailCandidates returns the global git user.email and the harness account
// email, de-duplicated case-insensitively, in that order.
//
// @spec: device-enrollment#email-candidates
func emailCandidates(ctx context.Context, account func() string) []string {
	var out []string
	add := func(e string) {
		e = strings.TrimSpace(e)
		if e == "" {
			return
		}
		for _, c := range out {
			if strings.EqualFold(c, e) {
				return
			}
		}
		out = append(out, e)
	}
	gctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if b, err := exec.CommandContext(gctx, "git", "config", "--global", "user.email").Output(); err == nil {
		add(string(b))
	}
	if account != nil {
		add(account())
	}
	return out
}

func promptEmail(out io.Writer, in *bufio.Reader, candidates []string) string {
	if len(candidates) == 0 {
		fmt.Fprint(out, "Your work email: ")
		return readLine(in)
	}
	fmt.Fprintln(out, "Which email should Neo.Tax know you by?")
	for i, c := range candidates {
		fmt.Fprintf(out, "  %d) %s\n", i+1, c)
	}
	fmt.Fprint(out, "Enter a number, or type another email: ")
	answer := readLine(in)
	if n, err := strconv.Atoi(answer); err == nil && n >= 1 && n <= len(candidates) {
		return candidates[n-1]
	}
	return answer
}

func readLine(r *bufio.Reader) string {
	s, _ := r.ReadString('\n')
	return strings.TrimSpace(s)
}
