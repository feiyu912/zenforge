package zenmind

import "strings"

type RouteDecision string

const (
	RouteLegacy   RouteDecision = "legacy"
	RouteZenForge RouteDecision = "zenforge"

	EngineLegacy   = "legacy"
	EngineZenForge = "zenforge"

	FeatureDisabled = "disabled"
	FeatureEnabled  = "enabled"
)

// RouteInput is the resolved platform identity and its explicit rollout values.
type RouteInput struct {
	AgentKey string
	ChatID   string
	RunID    string
	Engine   string
	Feature  string
}

type RouteConfig struct {
	Engine  string
	Feature string
}

type Router struct {
	AgentRoutes map[string]RouteConfig
	ChatRoutes  map[string]RouteConfig
	RunRoutes   map[string]RouteConfig
	Initialize  func(RouteInput) error

	// Deprecated compatibility fields for callers of Decide.
	Default    RouteDecision
	Agents     map[string]RouteDecision
	Sessions   map[string]RouteDecision
	MetaKey    string
	MetaEnable string
}

// Route fails closed. Only an explicit, supported ZenForge configuration whose
// initialization succeeds may leave the legacy path.
func (r Router) Route(input RouteInput) RouteDecision {
	config := RouteConfig{Engine: input.Engine, Feature: input.Feature}
	if selected, ok := lookupRoute(r.AgentRoutes, input.AgentKey); ok {
		config = selected
	}
	if selected, ok := lookupRoute(r.ChatRoutes, input.ChatID); ok {
		config = selected
	}
	if selected, ok := lookupRoute(r.RunRoutes, input.RunID); ok {
		config = selected
	}
	if config.Engine != EngineZenForge || config.Feature != FeatureEnabled || r.Initialize == nil {
		return RouteLegacy
	}
	input.AgentKey = strings.TrimSpace(input.AgentKey)
	input.ChatID = strings.TrimSpace(input.ChatID)
	input.RunID = strings.TrimSpace(input.RunID)
	if input.AgentKey == "" || input.ChatID == "" || input.RunID == "" {
		return RouteLegacy
	}
	input.Engine = config.Engine
	input.Feature = config.Feature
	if err := r.Initialize(input); err != nil {
		return RouteLegacy
	}
	return RouteZenForge
}

func lookupRoute(routes map[string]RouteConfig, key string) (RouteConfig, bool) {
	if key == "" {
		return RouteConfig{}, false
	}
	config, ok := routes[key]
	return config, ok
}

// Decide preserves the original adapter entry point. New integrations should
// pass the platform AgentKey, ChatID, and RunID directly to Route.
func (r Router) Decide(agent CatalogAgent, session Session) RouteDecision {
	input := RouteInput{
		AgentKey: routeIdentity(agent.Key, agent.AgentKey, agent.Name),
		ChatID:   routeIdentity(session.ChatID, session.ConversationID),
		RunID:    session.RunID,
	}
	input.Engine, input.Feature = r.compatibilityConfig(agent, session, input.AgentKey)
	if identityConflict(agent.Key, session.AgentKey) {
		return RouteLegacy
	}
	return r.Route(input)
}

func identityConflict(authoritative, requested string) bool {
	authoritative = strings.TrimSpace(authoritative)
	requested = strings.TrimSpace(requested)
	return authoritative != "" && requested != "" && authoritative != requested
}

func routeIdentity(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func (r Router) compatibilityConfig(agent CatalogAgent, session Session, agentKey string) (string, string) {
	if decision, ok := r.Sessions[session.RunID]; ok {
		return compatibilityValues(decision)
	}
	if decision, ok := r.Agents[agentKey]; ok {
		return compatibilityValues(decision)
	}
	if engine, feature, ok := explicitMetadataRoute(session.Metadata); ok {
		return engine, feature
	}
	if engine, feature, ok := explicitMetadataRoute(agent.Metadata); ok {
		return engine, feature
	}
	return compatibilityValues(r.Default)
}

func compatibilityValues(decision RouteDecision) (string, string) {
	if decision == RouteZenForge {
		return EngineZenForge, FeatureEnabled
	}
	if decision == RouteLegacy {
		return EngineLegacy, FeatureDisabled
	}
	return "", ""
}

func explicitMetadataRoute(meta map[string]any) (string, string, bool) {
	if meta == nil {
		return "", "", false
	}
	engine, engineOK := meta["engine"].(string)
	feature, featureOK := meta["feature"].(string)
	return engine, feature, engineOK && featureOK
}

func (d RouteDecision) UseZenForge() bool {
	return d == RouteZenForge
}
