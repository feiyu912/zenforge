package dshboot

import (
	"encoding/json"
	"regexp"
	"strings"
)

// queueScript is the inline classic script the host must place before the
// shell's own module script. It is copied verbatim from upstream
// (packages/client/modules/src/index.ts, bootInjections) because the shell's
// createClientModuleSystem call depends on this facade's exact shape: plugin
// bundles registering before the module system exists must queue, and
// create(options) materializes the client-modules bundle out of that queue. Any
// drift here is a total boot failure, not a degraded feature, so the text is
// copied rather than paraphrased.
const queueScript = `(()=>{
const pendingQueue=[]
window.__ModuleLoader__={
  mode:"queue",
  pendingQueue,
  load(registration){pendingQueue.push(registration)},
  create(options){
    if(this.mode!=="queue")throw new Error("client-modules: window.__ModuleLoader__.create called after module-system boot")
    const index=pendingQueue.findIndex(registration=>registration.id==="@deepseek-ai/dsh-client-modules")
    const registration=pendingQueue[index]
    if(registration===undefined)throw new Error("client-modules: HTML did not preload @deepseek-ai/dsh-client-modules/client.js")
    pendingQueue.splice(index,1)
    const exports=registration.factory(specifier=>{
      throw new Error('client-modules: @deepseek-ai/dsh-client-modules/client.js requested external "'+specifier+'" before the module system existed')
    })
    if(typeof exports!=="object"||exports===null||typeof exports.createClientModuleSystem!=="function"||typeof exports.apply!=="function"){
      throw new Error("client-modules: @deepseek-ai/dsh-client-modules/client.js did not export the bootstrap module face")
    }
    return exports.createClientModuleSystem(this,{id:registration.id,exports},options)
  }
}
})()`

// BootReadyScript is the tail script that settles the boot-readiness deferred.
// The shell awaits its promise before reading any injected state; because the
// served form has every row already in the document, it creates and resolves the
// deferred in one statement. It is a constant so re-injection can recognize an
// index that already carries the tail.
const BootReadyScript = `<script>(globalThis.__DSH_BOOT_READY__ ??= Promise.withResolvers()).resolve()</script>`

var (
	headTagPattern = regexp.MustCompile(`(?i)<head(?:\s[^>]*)?>`)
	bodyTagPattern = regexp.MustCompile(`(?i)<body(?:\s[^>]*)?>`)
)

// QueueScript returns the inline classic queue script without its script
// element, for callers that keep their own injection table.
func QueueScript() string {
	return queueScript
}

// HeadMarkup renders the head rows in their required order: the queue script
// first, then one preload per application batch, then one parser-blocking script
// per bootstrap batch, then the __DSH_BOOT__ global. The order is the contract:
// the queue facade must exist before any bundle executes, the preloads only warm
// the cache, the bootstrap script must run before the shell's module entry, and
// the graph must be readable by the time that entry runs. The rows are emitted
// without a placement so a caller can splice them wherever its document needs.
func HeadMarkup(graph *Graph) string {
	var out strings.Builder
	out.WriteString("<script>")
	out.WriteString(queueScript)
	out.WriteString("</script>")
	for _, batch := range graph.Batches {
		if batch.Phase == PhaseApplication {
			out.WriteString(`<link rel="preload" as="script" href="`)
			out.WriteString(escapeAttribute(batch.URL))
			out.WriteString(`">`)
		}
	}
	for _, batch := range graph.Batches {
		if batch.Phase == PhaseBootstrap {
			out.WriteString(`<script src="`)
			out.WriteString(escapeAttribute(batch.URL))
			out.WriteString(`"></script>`)
		}
	}
	out.WriteString(`<script>globalThis["__DSH_BOOT__"] = `)
	out.WriteString(jsonLiteral(graph))
	out.WriteString(`</script>`)
	return out.String()
}

// RenderInjections splices the boot rows into an HTML document: head rows
// immediately after the opening head tag, so they land before the shell's module
// script, and the readiness tail immediately after the opening body tag. A
// document without those tags receives the head rows at the front and the tail
// at the end, matching the host renderer the console is built against.
//
// Injection is idempotent: an index that already carries the queue script or the
// readiness tail is left alone, so a second render can never duplicate the
// globals or start two queues.
func RenderInjections(html string, graph *Graph) string {
	out := html
	if !strings.Contains(out, queueScript) {
		out = insertAfterTag(out, headTagPattern, HeadMarkup(graph), false)
	}
	if !strings.Contains(out, BootReadyScript) {
		out = insertAfterTag(out, bodyTagPattern, BootReadyScript, true)
	}
	return out
}

// insertAfterTag splices markup just past the end of the first matching opening
// tag. Headless or body-less fragments fall back to prepending or appending,
// which is where the HTML parser would synthesize the region anyway.
func insertAfterTag(html string, tag *regexp.Regexp, markup string, appendMissing bool) string {
	location := tag.FindStringIndex(html)
	if location == nil {
		if appendMissing {
			return html + markup
		}
		return markup + html
	}
	return html[:location[1]] + markup + html[location[1]:]
}

// escapeAttribute mirrors the host renderer's attribute escaping, and is applied
// after the ampersand replacement so an escaped entity is not escaped twice.
func escapeAttribute(value string) string {
	replacer := strings.NewReplacer(
		"&", "&amp;",
		`"`, "&quot;",
		"<", "&lt;",
		">", "&gt;",
	)
	return replacer.Replace(value)
}

// jsonLiteral renders a value as a JSON literal that cannot close the script
// element early: JSON's HTML-safety escaping already rewrites angle brackets,
// and the explicit replacement makes the guarantee independent of encoding
// defaults, since a revision is caller-supplied opaque text.
func jsonLiteral(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		// Unreachable for the closed wire graph. Emitting null keeps the failure
		// loud in the console (the boot manifest parser rejects it) instead of
		// silently booting a fabricated graph.
		return "null"
	}
	return strings.ReplaceAll(string(data), "<", `\u003c`)
}
