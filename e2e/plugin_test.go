package e2e

import (
	"encoding/json"
	"os"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/neo-tax/harness-plugins/internal/adapter/claudecode"
)

const pluginDir = "../plugins/claude-code"

type hooksFile struct {
	Hooks map[string][]struct {
		Matcher string `json:"matcher"`
		Hooks   []struct {
			Type    string `json:"type"`
			Command string `json:"command"`
			Async   bool   `json:"async"`
		} `json:"hooks"`
	} `json:"hooks"`
}

// @spec: claude-code-transcript-parser#hook-set
// @spec: claude-code-transcript-parser#hooks-are-async
// @spec: claude-code-transcript-parser#hook-exec-shape
func TestHookRegistrations(t *testing.T) {
	b, err := os.ReadFile(pluginDir + "/hooks/hooks.json")
	if err != nil {
		t.Fatal(err)
	}
	var f hooksFile
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	var names []string
	for name := range f.Hooks {
		names = append(names, name)
	}
	sort.Strings(names)
	want := append([]string{}, claudecode.Hooks...)
	sort.Strings(want)
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("registered %v, want %v", names, want)
	}
	for name, groups := range f.Hooks {
		if len(groups) != 1 || len(groups[0].Hooks) != 1 {
			t.Fatalf("%s: want exactly one hook", name)
		}
		h := groups[0].Hooks[0]
		if h.Type != "command" || !h.Async {
			t.Errorf("%s: type %q async %v", name, h.Type, h.Async)
		}
		shape := regexp.MustCompile(`^NEO_TAX_DATA_DIR="\$\{CLAUDE_PLUGIN_DATA\}" "\$\{CLAUDE_PLUGIN_ROOT\}/bin/neo-tax-agent" hook ` + name + `$`)
		if !shape.MatchString(h.Command) {
			t.Errorf("%s: command %q", name, h.Command)
		}
	}
}

// Each skill holds no logic: its only instruction to run anything is the
// agent command of the same name.
// @spec: device-enrollment#login-skill-delegates-to-agent
// @spec: event-queue-and-backfill#backfill-command
func TestSkillsDelegateToAgent(t *testing.T) {
	for _, name := range []string{"login", "backfill"} {
		b, err := os.ReadFile(pluginDir + "/skills/" + name + "/SKILL.md")
		if err != nil {
			t.Fatal(err)
		}
		s := string(b)
		if !strings.Contains(s, "\nname: "+name+"\n") {
			t.Errorf("skill is not named %s", name)
		}
		blocks := regexp.MustCompile("(?s)```bash\n(.*?)```").FindAllStringSubmatch(s, -1)
		want := map[string]string{"login": "login", "backfill": "backfill --background"}[name]
		if len(blocks) != 1 || strings.TrimSpace(blocks[0][1]) != `"${CLAUDE_SKILL_DIR}/../../bin/neo-tax-agent" `+want {
			t.Fatalf("%s code blocks %q", name, blocks)
		}
	}
}

// The login command waits up to five minutes for the browser form, and the
// model can relay the form's address only while it runs, so the skill runs
// it in the background; the model must never relay the token.
// @spec: device-enrollment#token-never-in-conversation
// @spec: device-enrollment#browser-wait-bounded
func TestLoginSkillWaitsForTheFormAndNeverAsksForTheToken(t *testing.T) {
	b, err := os.ReadFile(pluginDir + "/skills/login/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{"run_in_background", "Never ask the engineer for the enrollment token"} {
		if !strings.Contains(s, want) {
			t.Errorf("login skill lacks %q", want)
		}
	}
}

func TestManifestsNameThePluginNeoTax(t *testing.T) {
	var p struct{ Name, Version string }
	b, _ := os.ReadFile(pluginDir + "/.claude-plugin/plugin.json")
	json.Unmarshal(b, &p)
	var m struct {
		Name    string
		Plugins []struct{ Name, Version string }
	}
	b, _ = os.ReadFile("../.claude-plugin/marketplace.json")
	json.Unmarshal(b, &m)
	if p.Name != "neo-tax" || m.Name != "neo-tax-plugins" || len(m.Plugins) != 1 || m.Plugins[0].Name != "neo-tax" {
		t.Fatalf("plugin %q marketplace %q %+v", p.Name, m.Name, m.Plugins)
	}
}
