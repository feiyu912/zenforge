package zenmind

import "strings"

type RouteDecision string

const (
	RouteLegacy   RouteDecision = "legacy"
	RouteZenForge RouteDecision = "zenforge"
)

type Router struct {
	Default    RouteDecision
	Agents     map[string]RouteDecision
	Sessions   map[string]RouteDecision
	MetaKey    string
	MetaEnable string
}

func (r Router) Decide(agent CatalogAgent, session Session) RouteDecision {
	if decision, ok := r.Sessions[session.RunID]; ok && decision != "" {
		return decision
	}
	if decision, ok := r.Agents[agent.Name]; ok && decision != "" {
		return decision
	}
	metaKey := r.MetaKey
	if metaKey == "" {
		metaKey = "zenforge"
	}
	metaEnable := r.MetaEnable
	if metaEnable == "" {
		metaEnable = "enabled"
	}
	if metaDecision(agent.Metadata, metaKey, metaEnable) || metaDecision(session.Metadata, metaKey, metaEnable) {
		return RouteZenForge
	}
	if r.Default != "" {
		return r.Default
	}
	return RouteLegacy
}

func (d RouteDecision) UseZenForge() bool {
	return d == RouteZenForge
}

func metaDecision(meta map[string]any, key, enableValue string) bool {
	if meta == nil {
		return false
	}
	value, ok := meta[key]
	if !ok {
		return false
	}
	switch v := value.(type) {
	case bool:
		return v
	case string:
		normalized := strings.ToLower(strings.TrimSpace(v))
		return normalized == strings.ToLower(enableValue) ||
			normalized == "true" ||
			normalized == "1" ||
			normalized == "yes"
	default:
		return false
	}
}
