package zenmind

import (
	"context"
	"errors"
	"testing"
)

func initializedRouter() Router {
	return Router{Initialize: func(RouteInput) error { return nil }}
}

func TestRouterFailsClosed(t *testing.T) {
	tests := []struct {
		name   string
		input  RouteInput
		router Router
	}{
		{name: "missing configuration", input: RouteInput{}},
		{name: "missing feature", input: RouteInput{Engine: EngineZenForge}, router: initializedRouter()},
		{name: "unknown engine", input: RouteInput{Engine: "other", Feature: FeatureEnabled}, router: initializedRouter()},
		{name: "unknown feature", input: RouteInput{Engine: EngineZenForge, Feature: "preview"}, router: initializedRouter()},
		{name: "empty identity cannot match", router: Router{
			AgentRoutes: map[string]RouteConfig{"": {Engine: EngineZenForge, Feature: FeatureEnabled}},
			Initialize:  func(RouteInput) error { return nil },
		}},
		{name: "initializer missing", input: RouteInput{Engine: EngineZenForge, Feature: FeatureEnabled}},
		{
			name:  "initialization failure",
			input: RouteInput{Engine: EngineZenForge, Feature: FeatureEnabled},
			router: Router{Initialize: func(RouteInput) error {
				return errors.New("runtime unavailable")
			}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.router.Route(tt.input); got != RouteLegacy {
				t.Fatalf("Route() = %q, want %q", got, RouteLegacy)
			}
		})
	}
}

func TestRouterRoutesOnlyExplicitInitializedZenForge(t *testing.T) {
	input := RouteInput{
		AgentKey: "coder-app",
		ChatID:   "chat-1",
		RunID:    "run-1",
		Engine:   EngineZenForge,
		Feature:  FeatureEnabled,
	}
	seen := RouteInput{}
	router := Router{Initialize: func(got RouteInput) error {
		seen = got
		return nil
	}}
	if got := router.Route(input); got != RouteZenForge {
		t.Fatalf("Route() = %q, want %q", got, RouteZenForge)
	}
	if seen != input {
		t.Fatalf("initializer input = %#v, want %#v", seen, input)
	}
}

func TestRouterFallsBackWhenBuildRunInitializationFails(t *testing.T) {
	input := RouteInput{Engine: EngineZenForge, Feature: FeatureEnabled}
	tests := []struct {
		name    string
		agent   CatalogAgent
		runtime Runtime
	}{
		{name: "missing model"},
		{name: "missing catalog tool", agent: CatalogAgent{Tools: []string{"catalog-only"}}, runtime: runtimeWithModel()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			router := Router{Initialize: func(RouteInput) error {
				_, err := BuildRun(context.Background(), tt.agent, Session{Message: "hello"}, tt.runtime)
				return err
			}}
			if got := router.Route(input); got != RouteLegacy {
				t.Fatalf("Route() = %q, want fallback %q", got, RouteLegacy)
			}
		})
	}
}

func TestRouterRoutesBySessionThenAgent(t *testing.T) {
	legacy := RouteConfig{Engine: EngineLegacy, Feature: FeatureDisabled}
	zenforge := RouteConfig{Engine: EngineZenForge, Feature: FeatureEnabled}
	router := initializedRouter()
	router.AgentRoutes = map[string]RouteConfig{"agent-1": zenforge}
	router.ChatRoutes = map[string]RouteConfig{"chat-1": legacy}
	router.RunRoutes = map[string]RouteConfig{"run-1": zenforge}

	tests := []struct {
		name string
		in   RouteInput
		want RouteDecision
	}{
		{name: "input baseline", in: RouteInput{Engine: EngineLegacy, Feature: FeatureDisabled}, want: RouteLegacy},
		{name: "agent over input", in: RouteInput{AgentKey: "agent-1", Engine: EngineLegacy, Feature: FeatureDisabled}, want: RouteZenForge},
		{name: "chat over agent", in: RouteInput{AgentKey: "agent-1", ChatID: "chat-1"}, want: RouteLegacy},
		{name: "run over chat", in: RouteInput{AgentKey: "agent-1", ChatID: "chat-1", RunID: "run-1"}, want: RouteZenForge},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := router.Route(tt.in); got != tt.want {
				t.Fatalf("Route() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRouterTargetsAgentChatAndRunIDs(t *testing.T) {
	// Golden identity fixture: sibling protocol repository commit
	// 1893edb51b8dc691ae974cea2719a835e0e21de4, source
	// internal/api/types.go QueryRequest (agentKey, chatId, runId).
	fixture := RouteInput{AgentKey: "agent-a", ChatID: "chat-1", RunID: "run-1"}
	zenforge := RouteConfig{Engine: EngineZenForge, Feature: FeatureEnabled}

	tests := []struct {
		name   string
		router Router
		input  RouteInput
	}{
		{name: "agent", router: Router{AgentRoutes: map[string]RouteConfig{fixture.AgentKey: zenforge}}, input: RouteInput{AgentKey: fixture.AgentKey}},
		{name: "chat", router: Router{ChatRoutes: map[string]RouteConfig{fixture.ChatID: zenforge}}, input: RouteInput{ChatID: fixture.ChatID}},
		{name: "run", router: Router{RunRoutes: map[string]RouteConfig{fixture.RunID: zenforge}}, input: RouteInput{RunID: fixture.RunID}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.router.Initialize = func(RouteInput) error { return nil }
			if got := tt.router.Route(tt.input); got != RouteZenForge {
				t.Fatalf("Route() = %q, want %q", got, RouteZenForge)
			}
		})
	}
}

func TestRouterRoutesByMetadataFlag(t *testing.T) {
	router := initializedRouter()
	router.Agents = map[string]RouteDecision{"agent-a": RouteZenForge}
	if got := router.Decide(CatalogAgent{Name: "agent-a"}, Session{}); got != RouteZenForge {
		t.Fatalf("Decide() = %q, want %q", got, RouteZenForge)
	}

	router.Agents = nil
	agent := CatalogAgent{Name: "agent-a", Metadata: map[string]any{"engine": EngineZenForge, "feature": FeatureEnabled}}
	if got := router.Decide(agent, Session{}); got != RouteZenForge {
		t.Fatalf("Decide() explicit metadata = %q, want %q", got, RouteZenForge)
	}
	agent.Metadata = map[string]any{"zenforge": true}
	if got := router.Decide(agent, Session{}); got != RouteLegacy {
		t.Fatalf("Decide() permissive metadata = %q, want %q", got, RouteLegacy)
	}
	if !RouteZenForge.UseZenForge() || RouteLegacy.UseZenForge() {
		t.Fatal("UseZenForge returned unexpected values")
	}
}

func TestRouterDecidePrefersPlatformAgentKey(t *testing.T) {
	zenforge := RouteConfig{Engine: EngineZenForge, Feature: FeatureEnabled}
	legacy := RouteConfig{Engine: EngineLegacy, Feature: FeatureDisabled}
	router := initializedRouter()
	router.AgentRoutes = map[string]RouteConfig{
		"platform-key": zenforge,
		"request-key":  legacy,
		"legacy-name":  legacy,
	}

	agent := CatalogAgent{Key: "platform-key", AgentKey: "request-key", Name: "legacy-name"}
	if got := router.Decide(agent, Session{}); got != RouteZenForge {
		t.Fatalf("Decide() = %q, want Key route %q", got, RouteZenForge)
	}

	router.AgentRoutes["platform-key"] = legacy
	router.AgentRoutes["request-key"] = zenforge
	if got := router.Decide(CatalogAgent{AgentKey: "request-key", Name: "legacy-name"}, Session{}); got != RouteZenForge {
		t.Fatalf("Decide() = %q, want AgentKey fallback route %q", got, RouteZenForge)
	}
}

func TestRouterDecidePrefersPlatformChatID(t *testing.T) {
	zenforge := RouteConfig{Engine: EngineZenForge, Feature: FeatureEnabled}
	legacy := RouteConfig{Engine: EngineLegacy, Feature: FeatureDisabled}
	router := initializedRouter()
	router.ChatRoutes = map[string]RouteConfig{
		"platform-chat": zenforge,
		"legacy-chat":   legacy,
	}

	session := Session{ChatID: "platform-chat", ConversationID: "legacy-chat"}
	if got := router.Decide(CatalogAgent{}, session); got != RouteZenForge {
		t.Fatalf("Decide() = %q, want ChatID route %q", got, RouteZenForge)
	}

	router.ChatRoutes["platform-chat"] = legacy
	router.ChatRoutes["legacy-chat"] = zenforge
	if got := router.Decide(CatalogAgent{}, Session{ConversationID: "legacy-chat"}); got != RouteZenForge {
		t.Fatalf("Decide() = %q, want ConversationID fallback route %q", got, RouteZenForge)
	}
}
