package cli

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/feiyu912/zenforge/approval"
)

func writeApprovalConfigFile(t *testing.T, config configFile) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "zenforge.json")
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatalf("Marshal returned error: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	return path
}

func TestApprovalConfigParsesPersistenceKeys(t *testing.T) {
	grantsFile := filepath.Join(t.TempDir(), "grants.db")
	path := writeApprovalConfigFile(t, configFile{Approval: approvalConfig{
		Mode:       "prompt",
		GrantsFile: grantsFile,
		GrantTTL:   "12h",
		Tenant:     " tenant ",
		Subject:    " operator ",
	}})
	opts, err := optionsFromArgs([]string{"--config", path})
	if err != nil {
		t.Fatalf("optionsFromArgs returned error: %v", err)
	}
	if opts.approvalGrantsFile != grantsFile {
		t.Fatalf("grants file = %q", opts.approvalGrantsFile)
	}
	if opts.approvalGrantTTL != 12*time.Hour {
		t.Fatalf("grant ttl = %s", opts.approvalGrantTTL)
	}
	if opts.approvalTenant != "tenant" || opts.approvalSubject != "operator" {
		t.Fatalf("namespace = %q/%q", opts.approvalTenant, opts.approvalSubject)
	}
}

func TestApprovalConfigRejectsUnusablePersistence(t *testing.T) {
	dir := t.TempDir()
	grantsFile := filepath.Join(dir, "grants.db")
	cases := []struct {
		name    string
		config  approvalConfig
		wantErr string
	}{
		{
			name:    "an unparseable ttl is rejected",
			config:  approvalConfig{GrantsFile: grantsFile, GrantTTL: "later"},
			wantErr: "parse approval.grantTtl",
		},
		{
			name:    "a non-positive ttl is rejected",
			config:  approvalConfig{GrantsFile: grantsFile, GrantTTL: "0s"},
			wantErr: "approval.grantTtl must be positive",
		},
		{
			name:    "a ttl without a grants file is rejected",
			config:  approvalConfig{GrantTTL: "12h"},
			wantErr: "approval.grantTtl requires approval.grantsFile",
		},
		{
			name:    "a namespace without a grants file is rejected",
			config:  approvalConfig{Tenant: "tenant"},
			wantErr: "require approval.grantsFile",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			path := writeApprovalConfigFile(t, configFile{Approval: testCase.config})
			_, err := optionsFromArgs([]string{"--config", path})
			if err == nil || !strings.Contains(err.Error(), testCase.wantErr) {
				t.Fatalf("error = %v, want to contain %q", err, testCase.wantErr)
			}
		})
	}
}

func TestApprovalGrantConfigIsOptInAndSurvivesTheProcess(t *testing.T) {
	// Without a file there is no store and no namespace: a standing decision
	// stays in the run that made it.
	unconfigured := defaultOptions()
	store, namespace, err := approvalGrantConfig(&unconfigured)
	if err != nil {
		t.Fatalf("approvalGrantConfig returned error: %v", err)
	}
	if store != nil || namespace != (approval.Namespace{}) {
		t.Fatalf("an unconfigured client got %#v / %#v", store, namespace)
	}

	grantsFile := filepath.Join(t.TempDir(), "grants.db")
	first := defaultOptions()
	first.approvalGrantsFile = grantsFile
	store, namespace, err = approvalGrantConfig(&first)
	if err != nil {
		t.Fatalf("approvalGrantConfig returned error: %v", err)
	}
	if store == nil {
		t.Fatal("a configured grants file opened no store")
	}
	if namespace.Tenant != "cli" || namespace.Subject == "" {
		t.Fatalf("default namespace = %#v", namespace)
	}
	if len(first.closers) != 1 || first.closers[0].name != "approval grants" {
		t.Fatalf("the store is not owned by the command: %#v", first.closers)
	}
	grantedAt := time.Now().UTC().Truncate(time.Millisecond)
	if err := store.Put(context.Background(), approval.Grant{
		Namespace: namespace, Scope: approval.ScopeRule, RuleKey: "mcp:files:delete",
		Action: approval.DecisionApprove, GrantedAt: grantedAt,
	}); err != nil {
		t.Fatalf("Put returned error: %v", err)
	}
	// Closing is what the command's drain does; reopening is the next process.
	if err := first.closers[0].close(); err != nil {
		t.Fatalf("closing the store returned error: %v", err)
	}

	second := defaultOptions()
	second.approvalGrantsFile = grantsFile
	reopened, _, err := approvalGrantConfig(&second)
	if err != nil {
		t.Fatalf("reopening returned error: %v", err)
	}
	defer func() { _ = second.closers[0].close() }()
	stored, err := reopened.Get(context.Background(), namespace, "mcp:files:delete", "")
	if err != nil {
		t.Fatalf("the rule grant did not survive the process: %v", err)
	}
	if stored.EffectiveScope() != approval.ScopeRule || stored.Fingerprint != "" ||
		stored.Action != approval.DecisionApprove || !stored.GrantedAt.Equal(grantedAt) {
		t.Fatalf("stored grant = %#v", stored)
	}
}

func TestApprovalGrantConfigHonoursAnExplicitNamespace(t *testing.T) {
	opts := defaultOptions()
	opts.approvalGrantsFile = filepath.Join(t.TempDir(), "grants.db")
	opts.approvalTenant = "team"
	opts.approvalSubject = "agent-7"
	store, namespace, err := approvalGrantConfig(&opts)
	if err != nil {
		t.Fatalf("approvalGrantConfig returned error: %v", err)
	}
	defer func() { _ = store.(interface{ Close() error }).Close() }()
	if namespace.Tenant != "team" || namespace.Subject != "agent-7" {
		t.Fatalf("namespace = %#v", namespace)
	}
}

func TestBuildAgentOpensTheConfiguredGrantsFile(t *testing.T) {
	helper := newCLIMCPHelper(t)
	opts := defaultOptions()
	opts.apiKey = "test"
	opts.checkpointDir = t.TempDir()
	grantsFile := filepath.Join(t.TempDir(), "grants.db")
	opts.approvalGrantsFile = grantsFile
	opts.mcpServers = []mcpServerSpec{helper.spec("helper")}

	agent, err := buildAgent(context.Background(), &opts, IO{Stderr: io.Discard})
	if err != nil {
		t.Fatalf("buildAgent returned error: %v", err)
	}
	if agent == nil {
		t.Fatal("buildAgent returned no agent")
	}
	if _, err := os.Stat(grantsFile); err != nil {
		t.Fatalf("the configured grants file was not opened: %v", err)
	}
	drainClosers(&opts, IO{Stderr: io.Discard})
}
