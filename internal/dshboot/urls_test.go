package dshboot

import (
	"strings"
	"testing"
)

func TestComboURLShape(t *testing.T) {
	got := ComboURL([]string{"@a/x", "@b/y"}, "r1")
	want := "/plugins/??@a/x/client.js,@b/y/client.js&rev=r1"
	if got != want {
		t.Fatalf("ComboURL = %q, want %q", got, want)
	}
}

func TestComboURLAlwaysCarriesRevision(t *testing.T) {
	for _, rev := range []string{"", "r1", "a</script>"} {
		url := ComboURL([]string{"@a/x"}, rev)
		if !strings.Contains(url, "&rev=") {
			t.Fatalf("ComboURL(%q) = %q; no rev= parameter", rev, url)
		}
	}
}

func TestChunkURLShape(t *testing.T) {
	got := ChunkURL("@a/x", "client.abc123.js", "r1")
	want := "/plugins/@a/x/client.abc123.js?rev=r1"
	if got != want {
		t.Fatalf("ChunkURL = %q, want %q", got, want)
	}
}

func TestChunkURLAlwaysCarriesRevision(t *testing.T) {
	for _, rev := range []string{"", "r1"} {
		url := ChunkURL("@a/x", "client.abc123.js", rev)
		if !strings.Contains(url, "?rev=") {
			t.Fatalf("ChunkURL(%q) = %q; no rev= parameter", rev, url)
		}
	}
}

func TestBuiltURLsAlwaysCarryRevision(t *testing.T) {
	graph := mustGraph(t, []Entry{
		{ID: ClientModulesID, Rev: "rev-modules", Bundle: []byte("modules")},
		{ID: "@x/app", Rev: "rev-app", Bundle: []byte("app")},
	})
	for _, row := range graph.Entries {
		if !strings.Contains(row.URL, "&rev=") {
			t.Fatalf("entry %q URL %q has no rev=", row.ID, row.URL)
		}
	}
	for _, batch := range graph.Batches {
		if !strings.Contains(batch.URL, "&rev=") {
			t.Fatalf("batch %q URL %q has no rev=", batch.Phase, batch.URL)
		}
	}
}
