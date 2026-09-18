package dshboot

import (
	"strings"
	"testing"
)

const injectionFixture = `<!doctype html><html><head><title>console</title></head><body><div id="root"></div></body></html>`

func injectionGraph(t *testing.T) *Graph {
	t.Helper()
	return mustGraph(t, []Entry{
		{ID: ClientModulesID, Rev: "rev-modules", Bundle: []byte("modules")},
		{ID: "@x/app", Rev: "rev-app", Bundle: []byte("app")},
	})
}

func TestRenderInjectionsHeadOrder(t *testing.T) {
	out := RenderInjections(injectionFixture, injectionGraph(t))

	queue := strings.Index(out, `mode:"queue"`)
	preload := strings.Index(out, `<link rel="preload" as="script"`)
	bootstrap := strings.Index(out, `src="/plugins/??`+ClientModulesID+`/client.js`)
	boot := strings.Index(out, `globalThis["__DSH_BOOT__"]`)
	if queue < 0 || preload < 0 || bootstrap < 0 || boot < 0 {
		t.Fatalf("missing an injection row: queue=%d preload=%d bootstrap=%d boot=%d\n%s", queue, preload, bootstrap, boot, out)
	}
	if !(queue < preload && preload < bootstrap && bootstrap < boot) {
		t.Fatalf("head row order = queue:%d preload:%d bootstrap:%d boot:%d\n%s", queue, preload, bootstrap, boot, out)
	}
	head := strings.Index(out, "<head>")
	title := strings.Index(out, "<title>")
	if !(head < queue && queue < title) {
		t.Fatalf("head rows did not land between <head> and <title>: head=%d queue=%d title=%d", head, queue, title)
	}
}

func TestRenderInjectionsAddsBodyTailAndGlobals(t *testing.T) {
	out := RenderInjections(injectionFixture, injectionGraph(t))

	for _, global := range []string{"__ModuleLoader__", "__DSH_BOOT__", "__DSH_BOOT_READY__"} {
		if !strings.Contains(out, global) {
			t.Fatalf("injected document does not mention %s\n%s", global, out)
		}
	}
	body := strings.Index(out, "<body>")
	tail := strings.Index(out, BootReadyScript)
	root := strings.Index(out, `<div id="root">`)
	if tail < 0 {
		t.Fatalf("readiness tail missing\n%s", out)
	}
	if !(body < tail && tail < root) {
		t.Fatalf("tail placement = body:%d tail:%d root:%d\n%s", body, tail, root, out)
	}
}

func TestRenderInjectionsIsIdempotent(t *testing.T) {
	graph := injectionGraph(t)
	first := RenderInjections(injectionFixture, graph)
	second := RenderInjections(first, graph)
	if first != second {
		t.Fatalf("second injection changed the document\nfirst:  %s\nsecond: %s", first, second)
	}
	if count := strings.Count(second, `mode:"queue"`); count != 1 {
		t.Fatalf("queue script appears %d times, want 1", count)
	}
	if count := strings.Count(second, BootReadyScript); count != 1 {
		t.Fatalf("readiness tail appears %d times, want 1", count)
	}
	if count := strings.Count(second, `globalThis["__DSH_BOOT__"]`); count != 1 {
		t.Fatalf("boot global appears %d times, want 1", count)
	}
}

func TestRenderInjectionsEscapesScriptClose(t *testing.T) {
	hazard := `</script><script>alert(1)</script>`
	graph := mustGraph(t, []Entry{{ID: "@x/" + hazard, Rev: hazard, Bundle: []byte("evil")}})
	out := RenderInjections(injectionFixture, graph)

	if strings.Contains(out, "</script><script>alert(1)") {
		t.Fatalf("payload closed its script element\n%s", out)
	}
	if strings.Contains(out, "<script>alert(1)") {
		t.Fatalf("payload opened a new script element\n%s", out)
	}
	if !strings.Contains(out, `\u003c/script`) {
		t.Fatalf("JSON payload was not escaped\n%s", out)
	}
	if !strings.Contains(out, `&lt;/script&gt;`) {
		t.Fatalf("attribute payload was not escaped\n%s", out)
	}
}

func TestRenderInjectionsHandlesDocumentsWithoutHeadOrBody(t *testing.T) {
	out := RenderInjections("<div></div>", injectionGraph(t))
	if !strings.HasPrefix(out, "<script>") {
		t.Fatalf("headless document did not receive head rows first: %s", out)
	}
	if !strings.HasSuffix(out, BootReadyScript) {
		t.Fatalf("body-less document did not receive the tail last: %s", out)
	}
}
