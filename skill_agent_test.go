package zenforge

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	checkpointmemory "github.com/feiyu912/zenforge/checkpoint/memory"
	"github.com/feiyu912/zenforge/model"
	"github.com/feiyu912/zenforge/skill"
	"github.com/feiyu912/zenforge/subagent"
	"github.com/feiyu912/zenforge/tool"
)

func TestAgentSkillsUseProgressiveDisclosure(t *testing.T) {
	const (
		body   = "Use the release checklist in this complete instruction body."
		digest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	)
	bundle, err := skill.NewBundle(context.Background(), staticSkillCatalog{
		content: skill.Content{
			Descriptor: skill.Descriptor{Name: "release", Description: "Prepare a reliable release."},
			Body:       body,
			Digest:     digest,
			Provenance: skill.Provenance{Source: "test", Path: "skills/release/SKILL.md"},
		},
	}, nil)
	if err != nil {
		t.Fatalf("NewBundle returned error: %v", err)
	}

	fakeModel := &scriptedModel{turns: []scriptedTurn{
		{events: []model.Event{{Message: &model.Message{ToolCalls: []model.ToolCallSpec{{
			ID: "load_1", Name: "load_skill", Arguments: json.RawMessage(`{"name":"release"}`),
		}}}}}},
		{events: []model.Event{{Delta: "ready"}}},
	}}
	originalTools := make([]Tool, 0, 2)
	originalTools = append(originalTools, namedTool{name: "ordinary_tool"})
	config := Config{
		Model:        fakeModel,
		Instructions: "Keep answers concise.",
		Skills:       bundle,
		Tools:        originalTools,
	}
	agent := New(config)

	if config.Instructions != "Keep answers concise." || len(config.Tools) != 1 {
		t.Fatalf("New modified caller config: instructions=%q tools=%d", config.Instructions, len(config.Tools))
	}
	if originalTools[:cap(originalTools)][1] != nil {
		t.Fatal("New wrote load_skill into the caller's tool slice backing array")
	}

	result, err := agent.Run(context.Background(), Task{Input: "ship it"})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if result.Output != "ready" || len(fakeModel.requests) != 2 {
		t.Fatalf("result=%#v model requests=%d, want ready and 2 requests", result, len(fakeModel.requests))
	}

	first := fakeModel.requests[0]
	firstText := requestText(first)
	if !strings.Contains(firstText, "release") || !strings.Contains(firstText, "Prepare a reliable release.") {
		t.Fatalf("first request lacks skill name/description: %#v", first.Messages)
	}
	if strings.Contains(firstText, body) || strings.Contains(firstText, digest) ||
		strings.Contains(firstText, "skills/release/SKILL.md") {
		t.Fatalf("first request disclosed skill content or metadata: %#v", first.Messages)
	}
	if !hasTool(first.Tools, "load_skill") || !hasTool(first.Tools, "ordinary_tool") {
		t.Fatalf("first request tools = %#v", first.Tools)
	}

	secondText := requestText(fakeModel.requests[1])
	for _, want := range []string{body, digest, `"source":"test"`, `skills/release/SKILL.md`} {
		if !strings.Contains(secondText, want) {
			t.Fatalf("second request does not contain %q: %#v", want, fakeModel.requests[1].Messages)
		}
	}
}

func TestAgentSkillsRejectLoadSkillToolConflict(t *testing.T) {
	bundle, err := skill.NewBundle(context.Background(), staticSkillCatalog{
		content: skill.Content{
			Descriptor: skill.Descriptor{Name: "release", Description: "Release instructions."},
			Body:       "body",
			Digest:     "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			Provenance: skill.Provenance{Source: "test", Path: "release/SKILL.md"},
		},
	}, nil)
	if err != nil {
		t.Fatalf("NewBundle returned error: %v", err)
	}
	fakeModel := &scriptedModel{}
	agent := New(Config{
		Model:  fakeModel,
		Skills: bundle,
		Tools:  []Tool{namedTool{name: " LOAD_SKILL "}},
	})

	events, err := agent.Stream(context.Background(), Task{Input: "work"})
	if err == nil || !strings.Contains(err.Error(), "conflicts with the skill loader") {
		t.Fatalf("Stream events=%v error=%v, want explicit load_skill conflict", events, err)
	}
	if events != nil || len(fakeModel.requests) != 0 {
		t.Fatalf("conflicted agent started: events=%v requests=%d", events, len(fakeModel.requests))
	}
}

func TestAgentSkillsAreNotInheritedByChildAgents(t *testing.T) {
	const body = "parent-only complete instructions"
	bundle, err := skill.NewBundle(context.Background(), staticSkillCatalog{
		content: skill.Content{
			Descriptor: skill.Descriptor{Name: "release", Description: "Parent release instructions."},
			Body:       body,
			Digest:     "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			Provenance: skill.Provenance{Source: "test", Path: "release/SKILL.md"},
		},
	}, nil)
	if err != nil {
		t.Fatalf("NewBundle returned error: %v", err)
	}
	childModel := &scriptedModel{turns: []scriptedTurn{{events: []model.Event{{Delta: "done"}}}}}
	agent := New(Config{Model: childModel, Skills: bundle})

	_, err = agent.runChildSubAgent(
		context.Background(),
		subagent.SubAgentSpec{Name: "worker"},
		subagent.TaskSpec{ID: "child_1", AgentName: "worker", Input: "work"},
		subagent.Request{
			RunID:   "parent_1",
			Context: map[string]any{skillFingerprintMetaKey: bundle.Fingerprint()},
			Options: subagent.Options{InheritContext: true},
		},
	)
	if err != nil {
		t.Fatalf("runChildSubAgent returned error: %v", err)
	}
	if len(childModel.requests) != 1 {
		t.Fatalf("child model requests = %d, want 1", len(childModel.requests))
	}
	if strings.Contains(requestText(childModel.requests[0]), "Available skills:") ||
		strings.Contains(requestText(childModel.requests[0]), body) ||
		hasTool(childModel.requests[0].Tools, "load_skill") {
		t.Fatalf("child implicitly inherited parent skills: %#v", childModel.requests[0])
	}
	if _, ok := childModel.requests[0].Meta[skillFingerprintMetaKey]; ok {
		t.Fatalf("child inherited internal skill fingerprint: %#v", childModel.requests[0].Meta)
	}
}

func TestAgentSkillsCheckpointBindingDoesNotMutateTaskMeta(t *testing.T) {
	bundle := testSkillBundle(t, "body", "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	store := checkpointmemory.New()
	fakeModel := &scriptedModel{turns: []scriptedTurn{{events: []model.Event{{Delta: "done"}}}}}
	meta := map[string]any{"caller": "unchanged"}
	agent := New(Config{Model: fakeModel, Skills: bundle, Checkpoints: store})

	result, err := agent.Run(context.Background(), Task{RunID: "skills_meta", Input: "work", Meta: meta})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if _, ok := meta[skillFingerprintMetaKey]; ok || len(meta) != 1 {
		t.Fatalf("Run modified caller metadata: %#v", meta)
	}
	cp, err := store.Load(context.Background(), result.RunID)
	if err != nil {
		t.Fatalf("Load checkpoint: %v", err)
	}
	if got := cp.State.Meta[skillFingerprintMetaKey]; got != bundle.Fingerprint() {
		t.Fatalf("checkpoint fingerprint = %#v, want %q", got, bundle.Fingerprint())
	}
}

func TestAgentSkillsResumeIdentityBinding(t *testing.T) {
	original := testSkillBundle(t, "original", "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	changed := testSkillBundle(t, "changed", "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	extra, err := skill.NewBundle(context.Background(), multiSkillCatalog{
		"release": {
			Descriptor: skill.Descriptor{Name: "release", Description: "Release instructions."},
			Body:       "original", Digest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			Provenance: skill.Provenance{Source: "test", Path: "release/SKILL.md"},
		},
		"review": {
			Descriptor: skill.Descriptor{Name: "review", Description: "Review instructions."},
			Body:       "review", Digest: "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
			Provenance: skill.Provenance{Source: "test", Path: "review/SKILL.md"},
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name             string
		checkpointBundle *skill.Bundle
		currentBundle    *skill.Bundle
		wantError        string
	}{
		{name: "same bundle", checkpointBundle: original, currentBundle: original},
		{name: "content changed", checkpointBundle: original, currentBundle: changed, wantError: "fingerprint changed"},
		{name: "skills removed", checkpointBundle: original, wantError: "Config.Skills is not configured"},
		{name: "skills added", currentBundle: original, wantError: "checkpoint has no skills bundle"},
		{name: "extra skill", checkpointBundle: original, currentBundle: extra, wantError: "fingerprint changed"},
		{name: "legacy checkpoint without skills"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			const runID = "skills_resume"
			store := checkpointmemory.New()
			checkpointAgent := New(Config{Skills: test.checkpointBundle})
			cp := checkpointAgent.newCheckpoint(newRunState(runID, "resume me", nil), 1)
			if err := store.Save(context.Background(), cp); err != nil {
				t.Fatal(err)
			}
			fakeModel := &scriptedModel{turns: []scriptedTurn{{events: []model.Event{{Delta: "done"}}}}}
			agent := New(Config{Model: fakeModel, Skills: test.currentBundle, Checkpoints: store})

			events, err := agent.Resume(context.Background(), runID)
			if test.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantError) {
					t.Fatalf("Resume error = %v, want containing %q", err, test.wantError)
				}
				if events != nil || len(fakeModel.requests) != 0 {
					t.Fatalf("rejected resume executed work: events=%v requests=%d", events, len(fakeModel.requests))
				}
				return
			}
			if err != nil {
				t.Fatalf("Resume returned error: %v", err)
			}
			for range events {
			}
			if len(fakeModel.requests) == 0 {
				t.Fatal("compatible resume did not call model")
			}
		})
	}
}

func testSkillBundle(t *testing.T, body, digest string) *skill.Bundle {
	t.Helper()
	bundle, err := skill.NewBundle(context.Background(), staticSkillCatalog{content: skill.Content{
		Descriptor: skill.Descriptor{Name: "release", Description: "Release instructions."},
		Body:       body,
		Digest:     digest,
		Provenance: skill.Provenance{Source: "test", Path: "release/SKILL.md"},
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return bundle
}

type staticSkillCatalog struct {
	content skill.Content
}

func (c staticSkillCatalog) List(context.Context) ([]skill.Descriptor, error) {
	return []skill.Descriptor{c.content.Descriptor}, nil
}

type multiSkillCatalog map[string]skill.Content

func (c multiSkillCatalog) List(context.Context) ([]skill.Descriptor, error) {
	items := make([]skill.Descriptor, 0, len(c))
	for _, content := range c {
		items = append(items, content.Descriptor)
	}
	return items, nil
}

func (c multiSkillCatalog) Load(_ context.Context, name string) (skill.Content, error) {
	content, ok := c[name]
	if !ok {
		return skill.Content{}, skill.ErrNotFound
	}
	return content, nil
}

func (c staticSkillCatalog) Load(_ context.Context, name string) (skill.Content, error) {
	if name != c.content.Descriptor.Name {
		return skill.Content{}, skill.ErrNotFound
	}
	return c.content, nil
}

type namedTool struct {
	name string
}

func (t namedTool) Name() string         { return t.name }
func (namedTool) Description() string    { return "An ordinary typed tool." }
func (namedTool) Schema() map[string]any { return map[string]any{"type": "object"} }
func (namedTool) Call(context.Context, json.RawMessage, tool.Context) (tool.Result, error) {
	return tool.Result{Output: "ok"}, nil
}

func requestText(request model.Request) string {
	var out strings.Builder
	for _, message := range request.Messages {
		out.WriteString(message.Content)
		out.WriteByte('\n')
	}
	return out.String()
}
