package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/feiyu912/zenforge/approval"
	approvalsqlite "github.com/feiyu912/zenforge/approval/sqlite"
)

// seedGrants writes grants straight into a store file, which is what the agent
// would have left behind.
func seedGrants(t *testing.T, path string, grants ...approval.Grant) {
	t.Helper()
	store, err := approvalsqlite.Open(context.Background(), path)
	if err != nil {
		t.Fatalf("opening the seed store returned error: %v", err)
	}
	defer func() { _ = store.Close() }()
	for _, grant := range grants {
		if err := store.Put(context.Background(), grant); err != nil {
			t.Fatalf("Put returned error: %v", err)
		}
	}
}

func grantsConfigFile(t *testing.T, grantsFile string) string {
	t.Helper()
	return writeApprovalConfigFile(t, configFile{Approval: approvalConfig{GrantsFile: grantsFile}})
}

func standingGrant(namespace approval.Namespace, ruleKey string) approval.Grant {
	return approval.Grant{
		Namespace: namespace, Scope: approval.ScopeRule, RuleKey: ruleKey,
		Action: approval.DecisionApprove, GrantedAt: time.Now().UTC(),
	}
}

func pinnedGrant(namespace approval.Namespace, ruleKey, fingerprint string) approval.Grant {
	return approval.Grant{
		Namespace: namespace, Scope: approval.ScopeRun, RuleKey: ruleKey, Fingerprint: fingerprint,
		Action: approval.DecisionApprove, GrantedAt: time.Now().UTC(),
	}
}

func runGrants(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := Main(context.Background(), append([]string{"grants"}, args...), IO{
		Stdout: &stdout, Stderr: &stderr, Stdin: strings.NewReader(""),
	})
	return code, stdout.String(), stderr.String()
}

func TestGrantsCommandListsTheStandingGrant(t *testing.T) {
	dir := t.TempDir()
	grantsFile := filepath.Join(dir, "grants.db")
	namespace := approval.Namespace{Tenant: "cli", Subject: "operator"}
	seedGrants(t, grantsFile,
		standingGrant(namespace, "mcp:files:delete"),
		pinnedGrant(namespace, "mcp:files:write", "fp-1"),
		standingGrant(approval.Namespace{Tenant: "cli", Subject: "someone-else"}, "mcp:other:tool"),
	)
	configPath := grantsConfigFile(t, grantsFile)
	code, stdout, stderr := runGrants(t, "list", "--config", configPath, "--subject", "operator")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	if !strings.Contains(stdout, "mcp:files:delete") || !strings.Contains(stdout, "rule") {
		t.Fatalf("stdout = %q", stdout)
	}
	// A payload-pinned entry shares its rule key with a standing grant and is
	// shown as its own, labelled, entry.
	if !strings.Contains(stdout, "mcp:files:write (fp-1)") {
		t.Fatalf("a pinned grant was not distinguished: %q", stdout)
	}
	if strings.Contains(stdout, "mcp:other:tool") {
		t.Fatalf("another subject's grant leaked into the listing: %q", stdout)
	}
}

func TestGrantsCommandListsJSON(t *testing.T) {
	grantsFile := filepath.Join(t.TempDir(), "grants.db")
	namespace := approval.Namespace{Tenant: "cli", Subject: "operator"}
	seedGrants(t, grantsFile,
		standingGrant(namespace, "mcp:files:delete"),
		pinnedGrant(namespace, "mcp:files:write", "fp-1"),
	)
	configPath := grantsConfigFile(t, grantsFile)
	code, stdout, stderr := runGrants(t, "list", "--config", configPath, "--subject", "operator", "--json")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	var listed []approval.Grant
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &listed); err != nil {
		t.Fatalf("stdout is not JSON: %v (%q)", err, stdout)
	}
	if len(listed) != 2 {
		t.Fatalf("listed = %#v", listed)
	}
	// Sorted by rule key, and the scope is explicit so a reader does not have
	// to derive it from the fingerprint's absence.
	if listed[0].RuleKey != "mcp:files:delete" || listed[0].EffectiveScope() != approval.ScopeRule ||
		listed[1].RuleKey != "mcp:files:write" || listed[1].EffectiveScope() != approval.ScopeRun {
		t.Fatalf("listed = %#v", listed)
	}
}

func TestGrantsCommandRevokesAStandingGrant(t *testing.T) {
	grantsFile := filepath.Join(t.TempDir(), "grants.db")
	namespace := approval.Namespace{Tenant: "cli", Subject: "operator"}
	seedGrants(t, grantsFile, standingGrant(namespace, "mcp:files:delete"))
	configPath := grantsConfigFile(t, grantsFile)
	code, stdout, stderr := runGrants(t, "revoke", "mcp:files:delete", "--config", configPath, "--subject", "operator")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	if !strings.Contains(stdout, "revoked standing mcp:files:delete") {
		t.Fatalf("stdout = %q", stdout)
	}
	store, _, err := approvalGrantConfig(&options{approvalGrantsFile: grantsFile, approvalSubject: "operator"})
	if err != nil {
		t.Fatalf("reopening returned error: %v", err)
	}
	defer func() { _ = store.(interface{ Close() error }).Close() }()
	if _, err := store.Get(context.Background(), namespace, "mcp:files:delete", ""); !errors.Is(err, approval.ErrGrantNotFound) {
		t.Fatalf("the grant survived its revocation: %v", err)
	}
	code, stdout, stderr = runGrants(t, "list", "--config", configPath, "--subject", "operator")
	if code != 0 || !strings.Contains(stdout, "no grants for cli/operator") {
		t.Fatalf("code = %d, stdout = %q, stderr = %q", code, stdout, stderr)
	}
}

func TestGrantsCommandRevokesAPinnedGrantWithoutTheStandingOne(t *testing.T) {
	grantsFile := filepath.Join(t.TempDir(), "grants.db")
	namespace := approval.Namespace{Tenant: "cli", Subject: "operator"}
	seedGrants(t, grantsFile,
		standingGrant(namespace, "mcp:files:write"),
		pinnedGrant(namespace, "mcp:files:write", "fp-1"),
	)
	configPath := grantsConfigFile(t, grantsFile)
	code, stdout, stderr := runGrants(t, "revoke", "mcp:files:write", "--fingerprint", "fp-1",
		"--config", configPath, "--subject", "operator")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	if !strings.Contains(stdout, "revoked pinned mcp:files:write") {
		t.Fatalf("stdout = %q", stdout)
	}
	code, stdout, stderr = runGrants(t, "list", "--config", configPath, "--subject", "operator")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	if !strings.Contains(stdout, "mcp:files:write\trule") || strings.Contains(stdout, "fp-1") {
		t.Fatalf("the standing grant did not outlive the pinned one: %q", stdout)
	}
}

func TestGrantsCommandRevokesEveryGrant(t *testing.T) {
	grantsFile := filepath.Join(t.TempDir(), "grants.db")
	namespace := approval.Namespace{Tenant: "cli", Subject: "operator"}
	seedGrants(t, grantsFile,
		standingGrant(namespace, "mcp:files:delete"),
		pinnedGrant(namespace, "mcp:files:write", "fp-1"),
	)
	configPath := grantsConfigFile(t, grantsFile)
	code, stdout, stderr := runGrants(t, "revoke", "--all", "--config", configPath, "--subject", "operator")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	if !strings.Contains(stdout, "revoked 2 grant(s) for cli/operator") {
		t.Fatalf("stdout = %q", stdout)
	}
	code, stdout, stderr = runGrants(t, "list", "--config", configPath, "--subject", "operator")
	if code != 0 || !strings.Contains(stdout, "no grants for cli/operator") {
		t.Fatalf("code = %d, stdout = %q, stderr = %q", code, stdout, stderr)
	}
}

func TestGrantsCommandReportsAMissingGrant(t *testing.T) {
	grantsFile := filepath.Join(t.TempDir(), "grants.db")
	seedGrants(t, grantsFile, standingGrant(approval.Namespace{Tenant: "cli", Subject: "operator"}, "mcp:files:delete"))
	configPath := grantsConfigFile(t, grantsFile)
	code, _, stderr := runGrants(t, "revoke", "mcp:files:nope", "--config", configPath, "--subject", "operator")
	if code == 0 {
		t.Fatal("revoking a grant that is not there succeeded")
	}
	if !strings.Contains(stderr, `no standing grant for "mcp:files:nope" in cli/operator`) {
		t.Fatalf("stderr = %q", stderr)
	}
}

func TestGrantsCommandNeedsAConfiguredFile(t *testing.T) {
	configPath := writeApprovalConfigFile(t, configFile{})
	code, _, stderr := runGrants(t, "list", "--config", configPath)
	if code != exitInvalidUsage {
		t.Fatalf("code = %d, want %d; stderr = %q", code, exitInvalidUsage, stderr)
	}
	if !strings.Contains(stderr, "no approval grants file configured") {
		t.Fatalf("stderr = %q", stderr)
	}
}

func TestGrantsCommandUsage(t *testing.T) {
	grantsFile := filepath.Join(t.TempDir(), "grants.db")
	configPath := grantsConfigFile(t, grantsFile)
	cases := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{"no subcommand", []string{"--config", configPath}, "grants needs a subcommand"},
		{"unknown subcommand", []string{"bogus", "--config", configPath}, "unknown grants subcommand"},
		{"list with a positional", []string{"list", "extra", "--config", configPath}, "does not accept positional arguments"},
		{"revoke without a rule key", []string{"revoke", "--config", configPath}, "needs exactly one rule key"},
		{"revoke all with a rule key", []string{"revoke", "rule", "--all", "--config", configPath}, "does not accept a rule key"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			code, _, stderr := runGrants(t, testCase.args...)
			if code != exitInvalidUsage {
				t.Fatalf("code = %d, want %d; stderr = %q", code, exitInvalidUsage, stderr)
			}
			if !strings.Contains(stderr, testCase.wantErr) {
				t.Fatalf("stderr = %q, want to contain %q", stderr, testCase.wantErr)
			}
		})
	}
}

func TestGrantsCommandTakesTheFileAsAFlag(t *testing.T) {
	grantsFile := filepath.Join(t.TempDir(), "grants.db")
	namespace := approval.Namespace{Tenant: "cli", Subject: "operator"}
	seedGrants(t, grantsFile, standingGrant(namespace, "mcp:files:delete"))
	// The flag is what an operator with several grant files uses; the
	// namespace still comes from the same defaults.
	code, stdout, stderr := runGrants(t, "list", "--grants-file", grantsFile, "--subject", "operator")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	if !strings.Contains(stdout, "mcp:files:delete") {
		t.Fatalf("stdout = %q", stdout)
	}
	_ = io.Discard
}
