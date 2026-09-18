package dshboot

import (
	"fmt"
	"strings"
	"testing"
)

func mustGraph(t *testing.T, entries []Entry) *Graph {
	t.Helper()
	graph, err := BuildGraph(entries)
	if err != nil {
		t.Fatalf("BuildGraph: %v", err)
	}
	return graph
}

func graphEntry(id, rev string) GraphEntry {
	return GraphEntry{ID: id, Rev: rev, URL: ComboURL([]string{id}, rev)}
}

func TestBuildGraphProducesGraphTheConsoleValidates(t *testing.T) {
	graph := mustGraph(t, []Entry{
		{ID: ClientModulesID, Rev: "rev-modules", Bundle: []byte("modules")},
		{ID: "@x/app", Rev: "rev-app", Bundle: []byte("app")},
	})
	if err := ValidateGraph(graph); err != nil {
		t.Fatalf("ValidateGraph: %v", err)
	}
	if graph.Rev == "" {
		t.Fatal("graph revision is empty")
	}
	if len(graph.Batches) != 2 {
		t.Fatalf("batches = %d, want 2", len(graph.Batches))
	}
	if graph.Batches[0].Phase != PhaseBootstrap || graph.Batches[0].Entries[0] != ClientModulesID {
		t.Fatalf("first batch = %+v, want bootstrap %s", graph.Batches[0], ClientModulesID)
	}
	if graph.Batches[1].Phase != PhaseApplication {
		t.Fatalf("second batch phase = %q, want %q", graph.Batches[1].Phase, PhaseApplication)
	}
}

func TestBuildGraphRejectsDuplicateEntryID(t *testing.T) {
	_, err := BuildGraph([]Entry{
		{ID: "@x/app", Rev: "rev-1", Bundle: []byte("a")},
		{ID: "@x/app", Rev: "rev-2", Bundle: []byte("b")},
	})
	if err == nil {
		t.Fatal("BuildGraph accepted a duplicate entry id")
	}
	if !strings.Contains(err.Error(), `duplicate entry id "@x/app"`) {
		t.Fatalf("error does not name the duplicate id: %v", err)
	}
}

func TestBuildGraphOrdersModuleGraphDependencies(t *testing.T) {
	// The consumer is declared first; its External edge must move the provider
	// ahead of it, and the //client spelling must alias the bare package.
	graph := mustGraph(t, []Entry{
		{ID: "@x/ui", Rev: "rev-ui", Bundle: []byte("ui"), External: []string{"@x/core/client"}},
		{ID: "@x/core", Rev: "rev-core", Bundle: []byte("core")},
	})
	if graph.Entries[0].ID != "@x/core" || graph.Entries[1].ID != "@x/ui" {
		t.Fatalf("entry order = %q, %q; want provider first", graph.Entries[0].ID, graph.Entries[1].ID)
	}
	if graph.Batches[0].Entries[0] != "@x/core" {
		t.Fatalf("batch order = %v; want the provider first", graph.Batches[0].Entries)
	}
}

func TestBuildGraphRejectsModuleGraphCycle(t *testing.T) {
	_, err := BuildGraph([]Entry{
		{ID: "@x/a", Rev: "ra", Bundle: []byte("a"), External: []string{"@x/b"}},
		{ID: "@x/b", Rev: "rb", Bundle: []byte("b"), External: []string{"@x/a"}},
	})
	if err == nil {
		t.Fatal("BuildGraph accepted a cycle")
	}
	if !strings.Contains(err.Error(), "module graph cycle") {
		t.Fatalf("error does not describe the cycle: %v", err)
	}
}

func TestBuildGraphRejectsOversizedComboURL(t *testing.T) {
	huge := "@x/" + strings.Repeat("p", MaxComboURLBytes)
	_, err := BuildGraph([]Entry{{ID: huge, Rev: "rev", Bundle: []byte("a")}})
	if err == nil {
		t.Fatal("BuildGraph accepted an entry that cannot fit a combo URL")
	}
	if !strings.Contains(err.Error(), fmt.Sprintf("exceeds the %d-byte combo URL limit", MaxComboURLBytes)) {
		t.Fatalf("error does not describe the limit: %v", err)
	}
	if !strings.Contains(err.Error(), huge) {
		t.Fatalf("error does not name the offending id: %v", err)
	}
}

func TestValidateGraphRejectsEntryInTwoBatches(t *testing.T) {
	graph := &Graph{
		Rev:     "graph-rev",
		Entries: []GraphEntry{graphEntry("@x/a", "ra"), graphEntry("@x/b", "rb")},
		Batches: []Batch{
			{Phase: PhaseApplication, URL: "/plugins/??a&rev=ra", Rev: "ba", Entries: []string{"@x/a"}},
			{Phase: PhaseApplication, URL: "/plugins/??a&rev=ra2", Rev: "bb", Entries: []string{"@x/a"}},
		},
	}
	err := ValidateGraph(graph)
	if err == nil {
		t.Fatal("ValidateGraph accepted an entry in two batches")
	}
	if !strings.Contains(err.Error(), `entry "@x/a" belongs to more than one batch`) {
		t.Fatalf("error does not name the entry: %v", err)
	}
}

func TestValidateGraphRejectsEntryInNoBatch(t *testing.T) {
	graph := &Graph{
		Rev:     "graph-rev",
		Entries: []GraphEntry{graphEntry("@x/a", "ra"), graphEntry("@x/b", "rb")},
		Batches: []Batch{
			{Phase: PhaseApplication, URL: "/plugins/??a&rev=ra", Rev: "ba", Entries: []string{"@x/a"}},
		},
	}
	err := ValidateGraph(graph)
	if err == nil {
		t.Fatal("ValidateGraph accepted an entry in no batch")
	}
	if !strings.Contains(err.Error(), `entry "@x/b" belongs to no initial-load batch`) {
		t.Fatalf("error does not name the unbatched entry: %v", err)
	}
}

func TestValidateGraphRejectsDuplicateBatchURL(t *testing.T) {
	graph := &Graph{
		Rev:     "graph-rev",
		Entries: []GraphEntry{graphEntry("@x/a", "ra"), graphEntry("@x/b", "rb")},
		Batches: []Batch{
			{Phase: PhaseApplication, URL: "/plugins/??dup&rev=ba", Rev: "ba", Entries: []string{"@x/a"}},
			{Phase: PhaseApplication, URL: "/plugins/??dup&rev=ba", Rev: "bb", Entries: []string{"@x/b"}},
		},
	}
	err := ValidateGraph(graph)
	if err == nil {
		t.Fatal("ValidateGraph accepted a duplicate batch URL")
	}
	if !strings.Contains(err.Error(), `duplicate batch URL "/plugins/??dup&rev=ba"`) {
		t.Fatalf("error does not name the duplicate URL: %v", err)
	}
}

func TestValidateGraphRejectsUnknownBatchEntry(t *testing.T) {
	graph := &Graph{
		Rev:     "graph-rev",
		Entries: []GraphEntry{graphEntry("@x/a", "ra")},
		Batches: []Batch{
			{Phase: PhaseApplication, URL: "/plugins/??a&rev=ra", Rev: "ba", Entries: []string{"@x/missing"}},
		},
	}
	err := ValidateGraph(graph)
	if err == nil {
		t.Fatal("ValidateGraph accepted an unknown batch entry")
	}
	if !strings.Contains(err.Error(), `names unknown entry "@x/missing"`) {
		t.Fatalf("error does not name the unknown entry: %v", err)
	}
}

func TestValidateGraphRejectsBadPhase(t *testing.T) {
	graph := &Graph{
		Rev:     "graph-rev",
		Entries: []GraphEntry{graphEntry("@x/a", "ra")},
		Batches: []Batch{
			{Phase: "seed", URL: "/plugins/??a&rev=ra", Rev: "ba", Entries: []string{"@x/a"}},
		},
	}
	err := ValidateGraph(graph)
	if err == nil {
		t.Fatal("ValidateGraph accepted an unknown phase")
	}
	if !strings.Contains(err.Error(), `batch phase "seed"`) {
		t.Fatalf("error does not name the phase: %v", err)
	}
}

func TestBuildGraphRejectsMalformedChunkName(t *testing.T) {
	_, err := BuildGraph([]Entry{
		{ID: "@x/a", Rev: "ra", Bundle: []byte("a"), Chunks: []string{"chunk.js"}},
	})
	if err == nil {
		t.Fatal("BuildGraph accepted a non-client.<hash>.js chunk name")
	}
	if !strings.Contains(err.Error(), `entry "@x/a" chunk "chunk.js"`) {
		t.Fatalf("error does not name the entry and chunk: %v", err)
	}
}
