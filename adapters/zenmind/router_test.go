package zenmind

import "testing"

func TestRouterDefaultsToLegacy(t *testing.T) {
	if got := (Router{}).Decide(CatalogAgent{Name: "agent"}, Session{RunID: "run_1"}); got != RouteLegacy {
		t.Fatalf("decision = %s, want legacy", got)
	}
}

func TestRouterRoutesBySessionThenAgent(t *testing.T) {
	router := Router{
		Default:  RouteLegacy,
		Agents:   map[string]RouteDecision{"agent": RouteZenForge},
		Sessions: map[string]RouteDecision{"run_1": RouteLegacy},
	}
	if got := router.Decide(CatalogAgent{Name: "agent"}, Session{RunID: "run_1"}); got != RouteLegacy {
		t.Fatalf("session decision = %s, want legacy", got)
	}
	if got := router.Decide(CatalogAgent{Name: "agent"}, Session{RunID: "run_2"}); got != RouteZenForge {
		t.Fatalf("agent decision = %s, want zenforge", got)
	}
}

func TestRouterRoutesByMetadataFlag(t *testing.T) {
	router := Router{}
	if got := router.Decide(CatalogAgent{Name: "agent", Metadata: map[string]any{"zenforge": true}}, Session{}); got != RouteZenForge {
		t.Fatalf("agent meta decision = %s", got)
	}
	if got := router.Decide(CatalogAgent{Name: "agent"}, Session{Metadata: map[string]any{"zenforge": "enabled"}}); got != RouteZenForge {
		t.Fatalf("session meta decision = %s", got)
	}
	if !RouteZenForge.UseZenForge() || RouteLegacy.UseZenForge() {
		t.Fatalf("UseZenForge helper returned unexpected values")
	}
}
