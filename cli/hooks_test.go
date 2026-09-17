package cli

import (
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/feiyu912/zenforge/hooks"
)

func writeHooksFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "hooks.json")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	return path
}

func TestLoadHooksParsesTheReferenceShape(t *testing.T) {
	path := writeHooksFile(t, `{"hooks":{
		"PreToolUse":[{"matcher":"^shell$","command":"guard.sh","timeout":"5s","failClosed":true}],
		"post_tool_use":[{"command":"notify.sh"}]
	}}`)
	config, err := loadHooks(path)
	if err != nil {
		t.Fatalf("loadHooks returned error: %v", err)
	}
	pre := config[hooks.EventPreToolUse]
	if len(pre) != 1 || pre[0].Command != "guard.sh" || pre[0].Matcher != "^shell$" || !pre[0].FailClosed {
		t.Fatalf("pre hooks = %#v", pre)
	}
	if pre[0].Timeout.String() != "5s" {
		t.Fatalf("timeout = %s", pre[0].Timeout)
	}
	// Event names are accepted in either convention.
	if len(config[hooks.EventPostToolUse]) != 1 {
		t.Fatalf("post hooks = %#v", config[hooks.EventPostToolUse])
	}
	names := hookEventNames(config)
	if strings.Join(names, ",") != "PostToolUse,PreToolUse" {
		t.Fatalf("event names = %v", names)
	}
}

func TestLoadHooksRejectsBadConfiguration(t *testing.T) {
	cases := []struct {
		name    string
		content string
	}{
		{"unknown event", `{"hooks":{"BeforeTool":[{"command":"x"}]}}`},
		{"empty command", `{"hooks":{"Stop":[{"command":"  "}]}}`},
		{"unknown field", `{"hooks":{"Stop":[{"command":"x","whenever":true}]}}`},
		{"no hooks", `{"hooks":{}}`},
		{"malformed", `{"hooks":`},
		{"json array", `[]`},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			path := writeHooksFile(t, testCase.content)
			if _, err := loadHooks(path); err == nil {
				t.Fatalf("configuration %s was accepted", testCase.content)
			}
		})
	}
	if _, err := loadHooks(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Fatal("a missing hooks file was accepted")
	}
	if config, err := loadHooks("  "); err != nil || config != nil {
		t.Fatalf("an empty path returned %#v, %v", config, err)
	}
}

func TestHooksFlagIsBoundAndValidated(t *testing.T) {
	opts := defaultOptions()
	fs := flag.NewFlagSet("hooks-test", flag.ContinueOnError)
	bindOptions(fs, &opts)
	if err := fs.Parse([]string{"--hooks", "/tmp/hooks.json"}); err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if opts.hooksPath != "/tmp/hooks.json" {
		t.Fatalf("hooks path = %q", opts.hooksPath)
	}
	// A bad hooks file fails the command before any model call.
	path := writeHooksFile(t, `{"hooks":{"Nope":[{"command":"x"}]}}`)
	var stderr strings.Builder
	code := Main(context.Background(), []string{"run", "--hooks", path, "hello"}, IO{Stderr: &stderr})
	if code == 0 {
		t.Fatalf("a bad hooks file did not fail the run: %s", stderr.String())
	}
	if !strings.Contains(stderr.String(), "unknown hook event") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestHooksCommandJSONShape(t *testing.T) {
	// The on-disk shape is the contract with users, so it is pinned.
	command := hooks.Command{Matcher: "^shell$", Command: "guard.sh", Timeout: 0, FailClosed: true}
	encoded, err := json.Marshal(command)
	if err != nil {
		t.Fatalf("Marshal returned error: %v", err)
	}
	for _, field := range []string{`"matcher":"^shell$"`, `"command":"guard.sh"`, `"failClosed":true`} {
		if !strings.Contains(string(encoded), field) {
			t.Fatalf("encoded command %s is missing %s", encoded, field)
		}
	}
	if strings.Contains(string(encoded), "timeout") {
		t.Fatalf("a zero timeout should be omitted: %s", encoded)
	}
}
