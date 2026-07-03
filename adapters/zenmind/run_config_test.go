package zenmind

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/feiyu912/zenforge/approval"
	"github.com/feiyu912/zenforge/model"
	"github.com/feiyu912/zenforge/skill"
	"github.com/feiyu912/zenforge/subagent"
	"github.com/feiyu912/zenforge/tool"
	"github.com/feiyu912/zenforge/tools"
	"github.com/feiyu912/zenforge/workspace"
)

type stubWorkspace struct{ id string }

func (*stubWorkspace) Read(context.Context, string) ([]byte, error) { return nil, nil }
func (*stubWorkspace) Write(context.Context, string, []byte) error  { return nil }
func (*stubWorkspace) List(context.Context, string) ([]workspace.FileInfo, error) {
	return nil, nil
}
func (*stubWorkspace) Grep(context.Context, workspace.GrepQuery) ([]workspace.Match, error) {
	return nil, nil
}
func (*stubWorkspace) Stat(context.Context, string) (workspace.FileInfo, error) {
	return workspace.FileInfo{}, nil
}

type nilSkillResolver struct{}

func (*nilSkillResolver) ResolveSkills(context.Context, []string) (*skill.Bundle, error) {
	panic("typed-nil skill resolver must not be called")
}

type nilModelResolver struct{}

func (*nilModelResolver) ResolveModel(context.Context, string) (model.Model, error) {
	panic("typed-nil model resolver must not be called")
}

type nilToolResolver struct{}

func (*nilToolResolver) ResolveTools(context.Context, ToolResolution) ([]tool.Tool, error) {
	panic("typed-nil tool resolver must not be called")
}

type nilWorkspaceResolver struct{}

func (*nilWorkspaceResolver) ResolveWorkspace(context.Context, WorkspaceResolution) (workspace.Workspace, error) {
	panic("typed-nil workspace resolver must not be called")
}

func TestBuildRunMapsExecutableRuntimeConfig(t *testing.T) {
	bundle := &skill.Bundle{}
	resolvedWorkspace := &stubWorkspace{id: "session"}
	grants := approval.NewMemoryGrantStore()
	namespace := approval.Namespace{Tenant: "tenant", Subject: "subject"}
	redaction := []string{"password", "token"}
	specs := []subagent.SubAgentSpec{{
		Name:     "reviewer",
		Tools:    nil,
		Metadata: map[string]any{"owner": "host"},
	}}
	skillNames := []string{"go-review", "checkpoint"}
	readRoots := []string{"src"}
	writeRoots := []string{"generated"}
	overrides := map[string]any{"shell": map[string]any{"approval": true}}
	var gotSkillNames []string
	var gotWorkspace WorkspaceResolution
	var gotTools ToolResolution
	resolvedTool := tools.Must("shell", "test", func(context.Context, struct{}) (string, error) {
		return "", nil
	})

	run, err := BuildRun(context.Background(), CatalogAgent{
		Skills:        skillNames,
		Tools:         []string{"shell"},
		ToolOverrides: overrides,
		Workspace:     Workspace{Root: "/catalog"},
		HostAccess:    HostAccess{ReadRoots: readRoots, WriteRoots: writeRoots},
	}, Session{
		Message:       "review",
		WorkspaceRoot: "/session",
	}, Runtime{
		Model: &stubModel{key: "test"},
		SkillResolver: SkillResolverFunc(func(_ context.Context, names []string) (*skill.Bundle, error) {
			gotSkillNames = names
			return bundle, nil
		}),
		ToolResolver: ToolResolverFunc(func(_ context.Context, request ToolResolution) ([]tool.Tool, error) {
			gotTools = request
			return []tool.Tool{resolvedTool}, nil
		}),
		WorkspaceResolver: WorkspaceResolverFunc(func(_ context.Context, request WorkspaceResolution) (workspace.Workspace, error) {
			gotWorkspace = request
			return resolvedWorkspace, nil
		}),
		ToolArgumentRedaction: redaction,
		ApprovalGrants:        grants,
		ApprovalNamespace:     namespace,
		ApprovalGrantTTL:      15 * time.Minute,
		SubAgentSpecs:         specs,
		SubAgentOptions:       subagent.Options{MaxTasks: 3, Parallel: true},
	})
	if err != nil {
		t.Fatalf("BuildRun returned error: %v", err)
	}
	if run.Config.Skills != bundle || run.Config.Workspace != resolvedWorkspace {
		t.Fatalf("resolved executable config was not mapped: %#v", run.Config)
	}
	if len(run.Config.Tools) != 1 || run.Config.Tools[0] != resolvedTool ||
		len(gotTools.Names) != 1 || gotTools.Names[0] != "shell" {
		t.Fatalf("resolved tools were not mapped: config=%#v request=%#v", run.Config.Tools, gotTools)
	}
	if gotWorkspace.Root != "/session" {
		t.Fatalf("workspace root = %q, want session override", gotWorkspace.Root)
	}
	if len(gotWorkspace.ReadRoots) != 1 || gotWorkspace.ReadRoots[0] != "src" ||
		len(gotWorkspace.WriteRoots) != 1 || gotWorkspace.WriteRoots[0] != "generated" {
		t.Fatalf("host access was not mapped to workspace resolver: %#v", gotWorkspace)
	}
	if len(gotSkillNames) != 2 || gotSkillNames[0] != "go-review" {
		t.Fatalf("resolved skill names = %#v", gotSkillNames)
	}
	if run.Config.ApprovalGrants != grants || run.Config.ApprovalNamespace != namespace ||
		run.Config.ApprovalGrantTTL != 15*time.Minute {
		t.Fatalf("approval config was not mapped: %#v", run.Config)
	}
	if run.Config.SubAgentOptions.MaxTasks != 3 || !run.Config.SubAgentOptions.Parallel {
		t.Fatalf("sub-agent options were not mapped: %#v", run.Config.SubAgentOptions)
	}

	skillNames[0] = "changed"
	readRoots[0] = "changed"
	writeRoots[0] = "changed"
	overrides["shell"] = "changed"
	redaction[0] = "changed"
	specs[0].Name = "changed"
	specs[0].Metadata["owner"] = "changed"
	if gotSkillNames[0] != "go-review" {
		t.Fatalf("resolver skill names changed through caller slice: %#v", gotSkillNames)
	}
	if gotWorkspace.ReadRoots[0] != "src" || gotWorkspace.WriteRoots[0] != "generated" {
		t.Fatalf("workspace policy changed through caller slices: %#v", gotWorkspace)
	}
	if gotTools.Overrides["shell"].(map[string]any)["approval"] != true {
		t.Fatalf("tool overrides changed through caller map: %#v", gotTools.Overrides)
	}
	if run.Config.ToolArgumentRedaction[0] != "password" {
		t.Fatalf("redaction config changed through caller slice: %#v", run.Config.ToolArgumentRedaction)
	}
	if run.Config.SubAgentSpecs[0].Name != "reviewer" ||
		run.Config.SubAgentSpecs[0].Metadata["owner"] != "host" {
		t.Fatalf("sub-agent specs changed through caller data: %#v", run.Config.SubAgentSpecs)
	}
}

func TestBuildRunResolversFailClosed(t *testing.T) {
	tests := []struct {
		name    string
		agent   CatalogAgent
		runtime Runtime
		want    string
	}{
		{
			name: "typed nil model resolver", agent: CatalogAgent{ModelKey: "missing"},
			runtime: Runtime{ModelResolver: (*nilModelResolver)(nil)},
			want:    "requires a ModelResolver",
		},
		{
			name: "missing skill resolver", agent: CatalogAgent{Skills: []string{"missing"}},
			runtime: runtimeWithModel(), want: "require a SkillResolver",
		},
		{
			name: "typed nil skill resolver", agent: CatalogAgent{Skills: []string{"missing"}},
			runtime: Runtime{Model: &stubModel{}, SkillResolver: (*nilSkillResolver)(nil)},
			want:    "require a SkillResolver",
		},
		{
			name: "unknown skills", agent: CatalogAgent{Skills: []string{"missing"}},
			runtime: Runtime{Model: &stubModel{}, SkillResolver: SkillResolverFunc(func(context.Context, []string) (*skill.Bundle, error) {
				return nil, nil
			})},
			want: `unknown zenmind catalog skills ["missing"]`,
		},
		{
			name: "skill resolver error", agent: CatalogAgent{Skills: []string{"broken"}},
			runtime: Runtime{Model: &stubModel{}, SkillResolver: SkillResolverFunc(func(context.Context, []string) (*skill.Bundle, error) {
				return nil, errors.New("offline")
			})},
			want: "resolve zenmind catalog skills",
		},
		{
			name: "missing workspace resolver", agent: CatalogAgent{Workspace: Workspace{Root: "/missing"}},
			runtime: runtimeWithModel(), want: "requires a WorkspaceResolver",
		},
		{
			name: "host access without workspace resolver", agent: CatalogAgent{HostAccess: HostAccess{ReadRoots: []string{"src"}}},
			runtime: runtimeWithModel(), want: "requires a WorkspaceResolver",
		},
		{
			name: "tool overrides without tool resolver", agent: CatalogAgent{ToolOverrides: map[string]any{"shell": true}},
			runtime: runtimeWithModel(), want: "require a ToolResolver",
		},
		{
			name: "typed nil tool resolver", agent: CatalogAgent{ToolOverrides: map[string]any{"shell": true}},
			runtime: Runtime{Model: &stubModel{}, ToolResolver: (*nilToolResolver)(nil)},
			want:    "require a ToolResolver",
		},
		{
			name: "tool resolver returns no requested tools", agent: CatalogAgent{Tools: []string{"shell"}},
			runtime: Runtime{Model: &stubModel{}, ToolResolver: ToolResolverFunc(func(context.Context, ToolResolution) ([]tool.Tool, error) {
				return nil, nil
			})},
			want: "requested tools are unavailable",
		},
		{
			name: "typed nil workspace resolver", agent: CatalogAgent{Workspace: Workspace{Root: "/missing"}},
			runtime: Runtime{Model: &stubModel{}, WorkspaceResolver: (*nilWorkspaceResolver)(nil)},
			want:    "requires a WorkspaceResolver",
		},
		{
			name: "typed nil workspace result", agent: CatalogAgent{Workspace: Workspace{Root: "/missing"}},
			runtime: Runtime{Model: &stubModel{}, WorkspaceResolver: WorkspaceResolverFunc(func(context.Context, WorkspaceResolution) (workspace.Workspace, error) {
				var missing *stubWorkspace
				return missing, nil
			})},
			want: `unknown zenmind workspace "/missing"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := BuildRun(context.Background(), tt.agent, Session{Message: "run"}, tt.runtime)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestBuildRunExplicitEmptySkillsDisableRuntimeBundle(t *testing.T) {
	run, err := BuildRun(context.Background(), CatalogAgent{Skills: []string{}}, Session{Message: "run"}, Runtime{
		Model:  &stubModel{},
		Skills: &skill.Bundle{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if run.Config.Skills != nil {
		t.Fatalf("explicit empty catalog skills did not disable runtime bundle: %#v", run.Config.Skills)
	}
}

func TestBuildRunKeepsLegacyRuntimeWorkspaceWithoutRoot(t *testing.T) {
	legacy := &stubWorkspace{id: "legacy"}
	legacySkills := &skill.Bundle{}
	run, err := BuildRun(context.Background(), CatalogAgent{}, Session{Message: "run"}, Runtime{
		Model:     &stubModel{},
		Skills:    legacySkills,
		Workspace: legacy,
	})
	if err != nil {
		t.Fatalf("BuildRun returned error: %v", err)
	}
	if run.Config.Workspace != legacy {
		t.Fatalf("workspace = %#v, want legacy runtime workspace", run.Config.Workspace)
	}
	if run.Config.Skills != legacySkills {
		t.Fatalf("skills = %#v, want legacy runtime skills", run.Config.Skills)
	}
}
