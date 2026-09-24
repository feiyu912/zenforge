package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/feiyu912/zenforge"
	"github.com/feiyu912/zenforge/internal/dshstream"
	"github.com/feiyu912/zenforge/model"
	"github.com/feiyu912/zenforge/model/provider"
	"github.com/feiyu912/zenforge/server/auth"
	"github.com/feiyu912/zenforge/server/harnesshttp"
)

// The tests in this file make "the console is optional" a checked claim rather
// than a sentence (ADR 0099): they build the real serve assembly with
// --console=off and drive it through its own HTTP surface. Nothing here mocks
// the host's routes, and the only network is the local model stub.

// consoleServeApp builds the real newServeApp with an explicit --console
// decision and closes what it opened. The settings file is a caller's so a test
// that builds two hosts can point both at the same document.
func consoleServeApp(t *testing.T, opts *options, settingsFile, mode string) *serveApp {
	t.Helper()
	ioStreams := servedRunStreams()
	app, err := newServeApp(context.Background(), opts, ioStreams, serveConfig{
		settingsFile: settingsFile,
		console:      mode,
	})
	if err != nil {
		t.Fatalf("newServeApp with --console=%s: %v", mode, err)
	}
	t.Cleanup(func() {
		_ = app.Close(context.Background())
		drainClosers(opts, ioStreams)
	})
	return app
}

// TestConsoleOffHostServesAFullDetachedRun is the claim itself: a host that
// never builds dshmount, dshstream or the settings document still starts a real
// agent run over its harness API, on the model its flags named, and answers with
// what that model produced. The run is started, polled and read entirely through
// the public routes -- POST /runs/start, GET /runs/status, GET /runs/attach --
// and the model is a local httptest stub, so the whole path is hermetic.
//
// The document planted at --settings-file is the sharper half: it points at a
// dead endpoint and a different model and credential, and the run must ignore it
// and be answered by the flag endpoint. The bytes are compared afterwards, so a
// headless host neither reads the console's document nor rewrites it.
func TestConsoleOffHostServesAFullDetachedRun(t *testing.T) {
	t.Setenv("ZENFORGE_CONFIG_DIR", t.TempDir())
	const answer = "the answer from a console-off host"
	model := newOpenAISSEStub(t, textChunk(answer))
	opts := servedRunOptions(t, model.url)

	settingsFile := filepath.Join(t.TempDir(), "console-settings.json")
	planted := "http://127.0.0.1:1/v1"
	plantedModel, plantedKey := "model-only-the-document-knows", "sk-console-document-sentinel"
	if err := writeConsoleSettingsFile(settingsFile, consoleSettingsFile{
		BaseURL: &planted,
		Model:   &plantedModel,
		APIKey:  &plantedKey,
	}); err != nil {
		t.Fatalf("plant a console settings document at %s: %v", settingsFile, err)
	}
	before, err := os.ReadFile(settingsFile)
	if err != nil {
		t.Fatalf("read the planted document: %v", err)
	}

	app := consoleServeApp(t, &opts, settingsFile, serveConsoleOff)
	server := httptest.NewServer(app.Handler())
	t.Cleanup(server.Close)

	const runID = "run-headless"
	startDetachedRun(t, server.URL, runID)
	waitForDetachedRun(t, server.URL, runID, harnesshttp.RunCompleted)

	// The run's own transcript is the harness's, not the console's: attach
	// replays the durable log through the terminal event and returns.
	if got := detachedRunAnswer(t, server.URL, runID); got != answer {
		t.Fatalf("run %s answered %q, want %q from the flag endpoint", runID, got, answer)
	}

	// The detached run list is the same harness route the console's session list
	// is a projection of, so it has to answer on a host with no console.
	if runs := detachedRunIDs(t, server.URL); !runs[runID] {
		t.Fatalf("GET /runs does not list %s: %v", runID, runs)
	}

	after, err := os.ReadFile(settingsFile)
	if err != nil {
		t.Fatalf("read the document after the run: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("the headless host rewrote the console settings document:\nbefore: %s\nafter:  %s", before, after)
	}
}

// TestTheConsoleIsMountedOrNamedDisabled pins the boundary from both sides of the
// same assembly: with the console on, routes the coverage ledger lists as served
// answer and "/" is the injected shell; with it off, those same paths -- the
// settings-document API among them -- get the console_disabled envelope while the
// harness routes and the flag-only server-info route are untouched.
func TestTheConsoleIsMountedOrNamedDisabled(t *testing.T) {
	t.Setenv("ZENFORGE_CONFIG_DIR", t.TempDir())
	model := newOpenAISSEStub(t, textChunk("unused by this test"))
	settingsFile := filepath.Join(t.TempDir(), "console-settings.json")

	onOpts := servedRunOptions(t, model.url)
	on := consoleServeApp(t, &onOpts, settingsFile, serveConsoleOn)
	// session/list is a served method in docs/dsh-console-coverage.md: with the
	// console built it answers, and the root serves the injected shell.
	if recorder := consolePost(t, on, "session/list", "{}"); recorder.Code != http.StatusOK {
		t.Fatalf("console on: POST /api/session/list = %d: %s", recorder.Code, recorder.Body.String())
	}
	shell := consoleRequest(t, on, http.MethodGet, "/", "")
	if shell.Code != http.StatusOK || !strings.Contains(shell.Body.String(), "<title>zenforge</title>") {
		t.Fatalf("console on: GET / = %d, want the DSH shell: %s", shell.Code, shell.Body.String())
	}
	// /api/settings is the console's settings-document API, so a console host
	// answers it; that is the other side of the console-off refusal below.
	if settings := consoleRequest(t, on, http.MethodGet, "/api/settings", ""); settings.Code != http.StatusOK {
		t.Fatalf("console on: GET /api/settings = %d, want 200: %s", settings.Code, settings.Body.String())
	}

	offOpts := servedRunOptions(t, model.url)
	off := consoleServeApp(t, &offOpts, settingsFile, serveConsoleOff)

	// The same RPC, byte for byte, on the console-off host: the route is gone and
	// the answer says why rather than pretending net/http answered it.
	body := `{"type":"client-request","rpcId":"rpc-1","method":"session/list","payload":{"args":{}}}`
	recorder := consoleRequest(t, off, http.MethodPost, "/api/session/list", body)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("console off: POST /api/session/list = %d, want 404: %s", recorder.Code, recorder.Body.String())
	}
	assertConsoleDisabled(t, recorder)

	// Every other console surface a browser or the client could reach is the same
	// answer: the shell and a staged asset under the root catch-all, the WebSocket
	// mux the stream would have been mounted at, and the settings-document API --
	// whose POST would otherwise rewrite the model this host runs on.
	for _, target := range []string{
		"/",
		"/assets/app.js",
		"/plugins/roster.json",
		dshstream.MuxPath,
		"/api/settings",
		"/api/settings/describe",
	} {
		got := consoleRequest(t, off, http.MethodGet, target, "")
		if got.Code != http.StatusNotFound {
			t.Errorf("console off: GET %s = %d, want 404", target, got.Code)
			continue
		}
		assertConsoleDisabled(t, got)
	}
	post := consoleRequest(t, off, http.MethodPost, "/api/settings", `{"baseUrl":"https://example.invalid/v1","model":"m"}`)
	if post.Code != http.StatusNotFound {
		t.Errorf("console off: POST /api/settings = %d, want 404: %s", post.Code, post.Body.String())
	}
	assertConsoleDisabled(t, post)

	// /api/server reads only the flags, so it is not the console's and keeps
	// answering on the same host.
	serverInfo := consoleRequest(t, off, http.MethodGet, "/api/server", "")
	if serverInfo.Code != http.StatusOK {
		t.Errorf("console off: GET /api/server = %d, want 200: %s", serverInfo.Code, serverInfo.Body.String())
	}
}

// TestConsoleOffHostRefusesToStartWithoutAModel is the other half of removing the
// console from the model path: a headless host has no settings panel, so a host
// with no model must not start at all. It fails closed and names the flags,
// because the provider's own error names an environment variable an operator who
// passed --provider never used.
func TestConsoleOffHostRefusesToStartWithoutAModel(t *testing.T) {
	t.Setenv("ZENFORGE_CONFIG_DIR", t.TempDir())
	stub := newOpenAISSEStub(t, textChunk("unused"))
	noModel := servedRunOptions(t, stub.url)
	noModel.apiKey = ""
	// A variable this test owns and leaves empty, so the refusal cannot depend on
	// the operator's own environment.
	noModel.apiKeyEnv = "ZENFORGE_CONSOLE_OFF_TEST_API_KEY"
	t.Setenv(noModel.apiKeyEnv, "")

	_, err := newServeApp(context.Background(), &noModel, servedRunStreams(), serveConfig{console: serveConsoleOff})
	if err == nil {
		t.Fatal("a console-off host with no model configured started")
	}
	for _, want := range []string{"--console=off", "--provider", "--model", "--api-key", "--base-url"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %q: %v", want, err)
		}
	}

	// The console-on host keeps today's behaviour with the same flags: it starts,
	// and the operator fills the model in on the settings panel. This is the
	// contrast the refusal exists for, at the one place the two modes differ.
	consoleOpts := servedRunOptions(t, stub.url)
	consoleOpts.apiKey = ""
	consoleOpts.apiKeyEnv = "ZENFORGE_CONSOLE_OFF_TEST_API_KEY"
	app, err := newServeApp(context.Background(), &consoleOpts, servedRunStreams(), serveConfig{console: serveConsoleOn})
	if err != nil {
		t.Fatalf("a console host with no model configured refused to start: %v", err)
	}
	if err := app.Close(context.Background()); err != nil {
		t.Fatalf("close the console host: %v", err)
	}
	drainClosers(&consoleOpts, servedRunStreams())
}

// TestTheConsoleHostTakesItsModelFromTheSettingsDocument is the console-on side of
// the same seam: the flag endpoint and the document endpoint are two live stubs,
// and the run is answered by the document's. That is what makes the console-off
// test's "the document is ignored" a real difference rather than a difference in
// which stub a test happened to start.
func TestTheConsoleHostTakesItsModelFromTheSettingsDocument(t *testing.T) {
	t.Setenv("ZENFORGE_CONFIG_DIR", t.TempDir())
	flagStub := newOpenAISSEStub(t, textChunk("the flag endpoint answered"))
	documentStub := newOpenAISSEStub(t, textChunk("the settings document answered"))
	opts := servedRunOptions(t, flagStub.url)

	settingsFile := filepath.Join(t.TempDir(), "console-settings.json")
	documentURL, documentModel, documentKey := documentStub.url, "gpt-4.1", "sk-from-the-document"
	if err := writeConsoleSettingsFile(settingsFile, consoleSettingsFile{
		BaseURL: &documentURL,
		Model:   &documentModel,
		APIKey:  &documentKey,
	}); err != nil {
		t.Fatalf("write the console settings document: %v", err)
	}

	app := consoleServeApp(t, &opts, settingsFile, serveConsoleOn)
	server := httptest.NewServer(app.Handler())
	t.Cleanup(server.Close)

	const runID = "run-console-document"
	startDetachedRun(t, server.URL, runID)
	waitForDetachedRun(t, server.URL, runID, harnesshttp.RunCompleted)
	if got := detachedRunAnswer(t, server.URL, runID); got != "the settings document answered" {
		t.Fatalf("the run answered %q, want the settings document's endpoint", got)
	}
	if !strings.Contains(documentStub.body(), documentModel) {
		t.Fatalf("the document's model never reached its endpoint: %s", documentStub.body())
	}
	if flagStub.body() != "" {
		t.Fatalf("the run reached the flag endpoint even though the document configured the model: %s", flagStub.body())
	}
}

// TestConsoleOffHostResumesThroughTheCoreResolver pins what a checkpoint's frozen
// route resolves to on a host with no catalog. A console-off host leaves
// agentConfig.ModelResolver as buildAgentConfig set it -- the CLI's resolver -- and
// that resolver's answers are the limitation the ADR has to state:
//
//   - the host's own route rebuilds from the host's flags (endpoint and credential)
//     with the checkpoint's own model name, so a flag that names a different model
//     does not move a resumed run onto it;
//   - a provider the flags do not name and the console would have declared has no
//     answer here: the resolution fails by name instead of falling back.
func TestConsoleOffHostResumesThroughTheCoreResolver(t *testing.T) {
	t.Setenv("ZENFORGE_CONFIG_DIR", t.TempDir())
	stub := newOpenAISSEStub(t, textChunk("rebuilt by the core resolver"))
	opts := servedRunOptions(t, stub.url)
	resolver := cliModelResolver{opts: opts}

	adapter, err := resolver.Resolve(provider.OpenAI, "the-checkpoints-model")
	if err != nil {
		t.Fatalf("the host's own route did not resolve: %v", err)
	}
	response, err := adapter.Generate(context.Background(), model.Request{
		Messages: []model.Message{{Role: "user", Content: "hello"}},
	})
	if err != nil {
		t.Fatalf("the rebuilt adapter did not answer: %v", err)
	}
	if !strings.Contains(response.Message.Content, "rebuilt by the core resolver") {
		t.Fatalf("the rebuilt adapter answered %q", response.Message.Content)
	}
	if !strings.Contains(stub.body(), "the-checkpoints-model") {
		t.Fatalf("the checkpoint's model name did not reach the flag endpoint: %s", stub.body())
	}

	if _, err := resolver.Resolve("acme-gateway", "acme-1"); err == nil {
		t.Fatal("a route only the console could have declared resolved on a host with no console")
	} else if !strings.Contains(err.Error(), "acme-gateway") {
		t.Fatalf("the refusal does not name the route: %v", err)
	}
}

// TestConsoleOffHostKeepsItsSignInRoutesAndItsBoundary pins the two host routes
// around the console that survive --console=off when authentication is
// configured: the sign-in page stays reachable without a token, because it is how
// a browser gets one, and the disabled-console envelope is served to an admitted
// caller rather than to anyone who can reach the port -- the boundary wraps the
// fallback like every other route (ADR 0141).
func TestConsoleOffHostKeepsItsSignInRoutesAndItsBoundary(t *testing.T) {
	config := testServeAuth(t, true)
	secret := mintTestToken(t, config, "acme", "ci")
	handler := newServeMux(serveMuxConfig{
		auth:      config,
		workspace: "/tmp/zenforge-console-off",
		// No dsh, no stream and no settings store: this is the --console=off
		// route table, in which /api/settings is not registered and every console
		// path reaches the disabled envelope.
	})

	signIn := httptest.NewRecorder()
	handler.ServeHTTP(signIn, httptest.NewRequest(http.MethodGet, auth.SignInPath, nil))
	if signIn.Code != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200: a browser with no token has to reach the form that gives it one", auth.SignInPath, signIn.Code)
	}

	// The console being off is not an admission policy: an anonymous caller is
	// still refused before any route is chosen.
	anonymous := httptest.NewRecorder()
	handler.ServeHTTP(anonymous, httptest.NewRequest(http.MethodPost, "/api/session/list", nil))
	if anonymous.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous POST /api/session/list = %d, want 401", anonymous.Code)
	}

	// An admitted caller reaches the disabled-console envelope, not the console.
	admitted := httptest.NewRequest(http.MethodPost, "/api/session/list", nil)
	admitted.Header.Set("Authorization", "Bearer "+secret)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, admitted)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("admitted POST /api/session/list = %d, want 404: %s", recorder.Code, recorder.Body.String())
	}
	assertConsoleDisabled(t, recorder)
}

// TestServeRefusesAnUnknownConsoleMode pins the flag as a usage error rather
// than a host that quietly picks a mode. The accepted values are named, the way
// validateSandboxBackend names its own.
// TestServeRefusesASettingsFileItWouldIgnore pins the flag combination that
// would otherwise be dropped silently: --settings-file names the console's
// settings document, and a headless host neither reads nor writes one, so a
// deployment that passes both is told which half is wrong rather than started
// with a model configuration nobody chose.
func TestServeRefusesASettingsFileItWouldIgnore(t *testing.T) {
	var stderr bytes.Buffer
	err := serveCommand(context.Background(), []string{
		"--console=off",
		"--settings-file", filepath.Join(t.TempDir(), "console-settings.json"),
	}, IO{Stdout: io.Discard, Stderr: &stderr})
	if err == nil {
		t.Fatal("serve accepted --console=off with --settings-file")
	}
	if !errors.Is(err, errInvalidUsage) {
		t.Fatalf("the refusal is not a usage error: %v", err)
	}
	for _, want := range []string{"--settings-file", "--console=off", "--model"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %q: %v", want, err)
		}
	}

	// The same flag is the normal case with the console on, so the refusal is
	// about the combination and not about the flag.
	if err := validateConsoleMode(serveConsoleOn); err != nil {
		t.Fatalf("validateConsoleMode(on): %v", err)
	}
}

func TestServeRefusesAnUnknownConsoleMode(t *testing.T) {
	var stderr bytes.Buffer
	err := serveCommand(context.Background(), []string{"--console=maybe"}, IO{Stdout: io.Discard, Stderr: &stderr})
	if err == nil {
		t.Fatal("serve accepted --console=maybe")
	}
	if !errors.Is(err, errInvalidUsage) {
		t.Fatalf("--console=maybe is not a usage error: %v", err)
	}
	for _, want := range []string{"maybe", "on", "off"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %q: %v", want, err)
		}
	}

	// The default is on, and the zero serveConfig a test builds with means the
	// same thing the flag's default does.
	if err := validateConsoleMode(serveConsoleOn); err != nil {
		t.Errorf("validateConsoleMode(on): %v", err)
	}
	if err := validateConsoleMode(serveConsoleOff); err != nil {
		t.Errorf("validateConsoleMode(off): %v", err)
	}
	if !(serveConfig{}).consoleEnabled() {
		t.Error("the zero serveConfig does not serve the console")
	}
	if !consoleEnabled(serveConsoleOn) || consoleEnabled(serveConsoleOff) {
		t.Error("consoleEnabled does not follow the flag's two values")
	}
}

// TestServeComesUpHeadlessFromTheFlag walks the flag through the real command:
// --console=off parses, validates, reaches the assembly, and the host binds and
// serves until its context ends instead of failing on a nil console mount. The
// context is cancelled from the startup output, so the test does not sleep and
// does not guess when the listener is up.
func TestServeComesUpHeadlessFromTheFlag(t *testing.T) {
	t.Setenv("ZENFORGE_CONFIG_DIR", t.TempDir())
	args := []string{
		"--addr", "127.0.0.1:0",
		"--console=off",
		"--workspace", t.TempDir(),
		"--checkpoint-dir", t.TempDir(),
		"--api-key", "sk-headless-test",
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// The startup output is read through a pipe, and the cancel happens as soon
	// as the host says it is headless. The scanner keeps draining until
	// serveCommand returns, so the writer is never left blocked.
	reader, writer := io.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- serveCommand(ctx, args, IO{Stdout: writer, Stderr: io.Discard})
		_ = writer.Close()
	}()
	var output strings.Builder
	sawHeadless := false
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		line := scanner.Text()
		output.WriteString(line + "\n")
		if strings.Contains(line, "console: disabled") {
			sawHeadless = true
			cancel()
		}
	}
	if err := <-done; err != nil {
		t.Fatalf("serve --console=off returned error: %v", err)
	}
	if !sawHeadless {
		t.Fatalf("the headless host did not report its mode: %q", output.String())
	}
}

// TestConsoleOffHostAdoptsItsStoredRuns pins the durable registry as console
// independent: it is what the console's session list is projected from, and a
// headless host still records its runs in it and adopts the ones already on disk
// at startup. Without the adoption a restart with a lost registry would list
// nothing while every transcript stayed in the event store (ADR 0109).
func TestConsoleOffHostAdoptsItsStoredRuns(t *testing.T) {
	t.Setenv("ZENFORGE_CONFIG_DIR", t.TempDir())
	model := newOpenAISSEStub(t, textChunk("recorded by a headless host"))
	opts := servedRunOptions(t, model.url)
	settingsFile := filepath.Join(t.TempDir(), "console-settings.json")

	const runID = "run-adopted-headless"
	first := consoleServeApp(t, &opts, settingsFile, serveConsoleOff)
	server := httptest.NewServer(first.Handler())
	startDetachedRun(t, server.URL, runID)
	if got := detachedRunAnswer(t, server.URL, runID); got != "recorded by a headless host" {
		t.Fatalf("the headless run answered %q", got)
	}
	server.Close()
	if err := first.Close(context.Background()); err != nil {
		t.Fatalf("close the first headless host: %v", err)
	}

	// The install loses its registry the way one that predates it did: the
	// transcripts stay, the registry rows do not.
	registryPath, err := runRegistryPath(&opts)
	if err != nil {
		t.Fatalf("runRegistryPath: %v", err)
	}
	if err := os.Remove(registryPath); err != nil {
		t.Fatalf("remove the registry: %v", err)
	}

	consoleServeApp(t, &opts, settingsFile, serveConsoleOff)
	registry, err := harnesshttp.OpenSQLiteRunRegistry(context.Background(), registryPath)
	if err != nil {
		t.Fatalf("reopen the registry: %v", err)
	}
	defer func() { _ = registry.Close() }()
	info, err := registry.Get(context.Background(), runID)
	if err != nil {
		t.Fatalf("the restarted headless host did not adopt %s: %v", runID, err)
	}
	if info.Status != harnesshttp.RunCompleted {
		t.Fatalf("the adopted run is recorded as %q, want %q", info.Status, harnesshttp.RunCompleted)
	}
}

// consoleRequest drives the assembled handler with a request that looks like a
// loopback browser's, which is what the console's own helpers do.
func consoleRequest(t *testing.T, app *serveApp, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	request := httptest.NewRequest(method, target, reader)
	request.Host = "127.0.0.1:8787"
	request.RemoteAddr = "127.0.0.1:54321"
	recorder := httptest.NewRecorder()
	app.Handler().ServeHTTP(recorder, request)
	return recorder
}

// assertConsoleDisabled checks the exact answer a console-off host gives for a
// path it does not serve: the host's JSON envelope, the console_disabled code,
// and a message that names the flag.
func assertConsoleDisabled(t *testing.T, recorder *httptest.ResponseRecorder) {
	t.Helper()
	if contentType := recorder.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "application/json") {
		t.Errorf("the disabled answer is %q, want the host's JSON envelope", contentType)
	}
	if body := recorder.Body.String(); strings.Contains(body, "<html") || strings.Contains(body, "<!DOCTYPE") {
		t.Errorf("a console-off host served HTML for a disabled console path: %s", body)
	}
	var envelope struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode the disabled answer %q: %v", recorder.Body.String(), err)
	}
	if envelope.Error.Code != consoleDisabledCode {
		t.Errorf("code = %q, want %q", envelope.Error.Code, consoleDisabledCode)
	}
	if !strings.Contains(envelope.Error.Message, "--console=off") {
		t.Errorf("the disabled answer does not name the flag: %q", envelope.Error.Message)
	}
}

// startDetachedRun starts one run through the harness's public start route.
func startDetachedRun(t *testing.T, baseURL, runID string) {
	t.Helper()
	body := fmt.Sprintf(`{"runId":%q,"input":"say hello"}`, runID)
	response, err := http.Post(baseURL+"/runs/start", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST /runs/start: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	detail, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("POST /runs/start = %d, want 202: %s", response.StatusCode, detail)
	}
}

// waitForDetachedRun polls the harness status route until the run reaches want,
// in the same twenty-millisecond loop the console helpers use. A run that ends
// in another terminal state fails rather than waiting out the deadline.
func waitForDetachedRun(t *testing.T, baseURL, runID string, want harnesshttp.RunStatus) harnesshttp.RunInfo {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	var last harnesshttp.RunInfo
	for time.Now().Before(deadline) {
		response, err := http.Get(baseURL + "/runs/status?runId=" + runID)
		if err != nil {
			t.Fatalf("GET /runs/status: %v", err)
		}
		body, _ := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("GET /runs/status = %d: %s", response.StatusCode, body)
		}
		if err := json.Unmarshal(body, &last); err != nil {
			t.Fatalf("decode run status %q: %v", body, err)
		}
		switch last.Status {
		case want:
			return last
		case harnesshttp.RunFailed, harnesshttp.RunCancelled:
			t.Fatalf("run %s ended %q, want %q: %s", runID, last.Status, want, body)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("run %s never reached %q (last %+v)", runID, want, last)
	return last
}

// detachedRunAnswer reads a finished run's transcript over the harness's own
// attach stream. The stream replays the durable log and ends at the terminal
// event, so the answer is whatever the run's own run.done recorded -- the
// harness's output, with no console projection involved.
func detachedRunAnswer(t *testing.T, baseURL, runID string) string {
	t.Helper()
	response, err := http.Get(baseURL + "/runs/attach?runId=" + runID)
	if err != nil {
		t.Fatalf("GET /runs/attach: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		detail, _ := io.ReadAll(response.Body)
		t.Fatalf("GET /runs/attach = %d: %s", response.StatusCode, detail)
	}
	answer := ""
	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var event struct {
			Type   string `json:"type"`
			Output string `json:"output"`
		}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event); err != nil {
			continue
		}
		if event.Type == string(zenforge.EventRunDone) {
			answer = event.Output
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("read the attach stream for %s: %v", runID, err)
	}
	return answer
}

// detachedRunIDs is the harness route that lists the runs this host knows,
// reduced to the ids the test cares about.
func detachedRunIDs(t *testing.T, baseURL string) map[string]bool {
	t.Helper()
	response, err := http.Get(baseURL + "/runs")
	if err != nil {
		t.Fatalf("GET /runs: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET /runs = %d: %s", response.StatusCode, body)
	}
	var listing struct {
		Runs []struct {
			RunID string `json:"runId"`
		} `json:"runs"`
	}
	if err := json.Unmarshal(body, &listing); err != nil {
		t.Fatalf("decode GET /runs %q: %v", body, err)
	}
	runs := make(map[string]bool, len(listing.Runs))
	for _, run := range listing.Runs {
		runs[run.RunID] = true
	}
	return runs
}
