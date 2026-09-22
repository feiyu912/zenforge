package dshapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// goalRemotePath is the vendored console's generated remote map. Every envelope
// in goals.go is a claim about these bytes, so the test below re-derives the
// shapes from them instead of restating them: a console upgrade that widens,
// narrows or renames a goal parameter must fail here rather than reach the dock
// as a runtime surprise.
const goalRemotePath = "../../webui/dsh/plugins/api/remotes/client.js"

// The seven methods the goal domain exposes, in the order the reference declares
// them.
var goalMethods = []string{"clear", "complete", "create", "edit", "get", "pause", "resume"}

// goalHandlerFunc names the handler function that serves each method, so the
// argument names this host accepts can be read from this repository's own source
// and compared with the console's.
var goalHandlerFunc = map[string]string{
	"get":      "goalsGet",
	"create":   "goalsCreate",
	"edit":     "goalsEdit",
	"pause":    "goalsPause",
	"resume":   "goalsResume",
	"complete": "goalsComplete",
	"clear":    "goalsClear",
}

// goalViewKeys is the result object of six of the seven methods, and the object
// inside `get`'s union with undefined.
var goalViewKeys = []string{
	"activation", "blockedReason", "createdAt", "id", "maxGoalRounds",
	"objective", "phase", "revision", "roundsStarted", "updatedAt",
}

// TestGoalEnvelopesMatchTheVendoredConsole is the boundary check for this
// namespace. For each method it compares four things, all derived rather than
// restated:
//
//   - the parameter wire names the client sends (`agentId`, `ref`, `request`),
//   - the object parameters' keys (the create/edit request, the ref),
//   - the result's keys and whether the result admits undefined (`get`),
//   - the argument names this host's own handler accepts, read out of goals.go.
//
// The fourth is what catches the interesting mistake: a wire name that reaches
// the host and is refused as unknown is exactly the failure a schema-only check
// cannot see.
func TestGoalEnvelopesMatchTheVendoredConsole(t *testing.T) {
	source := readSource(t, goalRemotePath)
	recipe := readSource(t, "goals.go")

	// The ref object is shared by clear/complete/pause/resume parameters and
	// results, and by create's `{ref}` result.
	refKeys := []string{"id", "revision"}
	createRequestKeys := []string{"maxGoalRounds", "objective"}
	editRequestKeys := []string{"maxGoalRounds", "objective"}

	cases := []struct {
		method          string
		wires           []string
		requestKeys     []string // keys of the `request` parameter, when it has one
		resultKeys      []string
		resultUndefined bool
	}{
		{"clear", []string{"agentId", "ref"}, nil, refKeys, false},
		{"complete", []string{"agentId", "ref"}, nil, goalViewKeys, false},
		{"create", []string{"agentId", "request"}, createRequestKeys, []string{"ref"}, false},
		{"edit", []string{"agentId", "ref", "request"}, editRequestKeys, goalViewKeys, false},
		{"get", []string{"agentId"}, nil, goalViewKeys, true},
		{"pause", []string{"agentId", "ref"}, nil, goalViewKeys, false},
		{"resume", []string{"agentId", "ref"}, nil, goalViewKeys, false},
	}
	if len(cases) != len(goalMethods) {
		t.Fatalf("the table covers %d methods and the domain declares %d", len(cases), len(goalMethods))
	}

	for _, tc := range cases {
		t.Run(tc.method, func(t *testing.T) {
			descriptor := goalDescriptor(t, source, tc.method)
			wires := goalWireNames(t, descriptor, tc.method)
			if !sameStrings(wires, tc.wires) {
				t.Fatalf("console wires = %v, want %v", wires, tc.wires)
			}

			// The result schema, and whether the method answers undefined.
			result := goalSchemaExpression(t, source, tc.method, "result")
			if keys := sortedKeys(topLevelKeys(t, result)); !sameStrings(keys, tc.resultKeys) {
				t.Fatalf("%s result keys = %v, want %v", tc.method, keys, tc.resultKeys)
			}
			if got := strings.Contains(result, "union([_undefined()"); got != tc.resultUndefined {
				t.Fatalf("%s result admits undefined = %v, want %v", tc.method, got, tc.resultUndefined)
			}

			// The object parameters, in declaration order.
			objects := goalObjectParameters(t, source, tc.method)
			wantObjects := 0
			if tc.requestKeys != nil {
				wantObjects++
			}
			if containsString(tc.wires, "ref") {
				wantObjects++
			}
			if len(objects) != wantObjects {
				t.Fatalf("%s object parameters = %v, want %d", tc.method, objects, wantObjects)
			}
			for _, object := range objects {
				keys := sortedKeys(object)
				switch {
				case len(keys) == 2 && keys[0] == "id" && keys[1] == "revision":
					// The compare-and-set ref.
				case tc.requestKeys != nil && sameStrings(keys, tc.requestKeys):
				default:
					t.Fatalf("%s object parameter keys = %v, want the ref or the request", tc.method, keys)
				}
			}

			// What this host's handler accepts, read from its own source.
			accepted := handlerArgumentNames(t, recipe, goalHandlerFunc[tc.method])
			expected := append([]string{}, tc.wires...)
			if tc.requestKeys != nil {
				expected = removeString(expected, "request")
				expected = append(expected, tc.requestKeys...)
			}
			if !sameStrings(accepted, expected) {
				t.Fatalf("%s accepts %v, want %v", tc.method, accepted, expected)
			}
		})
	}
}

// TestGoalEnvelopeValuesMatchTheVendoredConsole checks that the Go types this
// host serves marshal exactly the keys the console validates. A field added here
// and not declared there is a strict-mode rejection at the client, and a field
// dropped here is one the dock renders as undefined.
func TestGoalEnvelopeValuesMatchTheVendoredConsole(t *testing.T) {
	source := readSource(t, goalRemotePath)
	view := GoalView{
		ID:            "goal-1",
		Revision:      2,
		Objective:     "ship the goal dock",
		Phase:         "paused",
		BlockedReason: &GoalBlockedReason{Code: "blocked-rounds-remaining", Message: "waiting"},
		MaxGoalRounds: 8,
		RoundsStarted: 1,
		CreatedAt:     1_700_000_000_000,
		UpdatedAt:     1_700_000_060_000,
		Activation:    "armed",
	}
	ref := GoalRef{ID: "goal-1", Revision: 2}

	for _, tc := range []struct {
		method string
		value  any
	}{
		{"clear", ref},
		{"complete", view},
		{"create", CreateGoalResult{Ref: ref}},
		{"edit", view},
		{"get", view},
		{"pause", view},
		{"resume", view},
	} {
		t.Run(tc.method, func(t *testing.T) {
			want := sortedKeys(topLevelKeys(t, goalSchemaExpression(t, source, tc.method, "result")))
			if got := sortedKeys(jsonKeys(t, tc.value)); !sameStrings(got, want) {
				t.Fatalf("%s result keys = %v, want %v", tc.method, got, want)
			}
		})
	}

	// The requests are the same comparison in the other direction: what this host
	// declares to the store is what the client declares on the wire. Both round
	// caps are optional there and omitted here when zero, so the samples set them.
	for _, tc := range []struct {
		method    string
		parameter string
		value     any
	}{
		{"create", "parameter_1", CreateGoalRequest{Objective: "ship it", MaxGoalRounds: 8}},
		{"edit", "parameter_2", EditGoalRequest{Objective: stringPointer("ship it"), MaxGoalRounds: intPointer(8)}},
	} {
		want := sortedKeys(topLevelKeys(t, goalSchemaExpression(t, source, tc.method, tc.parameter)))
		if got := sortedKeys(jsonKeys(t, tc.value)); !sameStrings(got, want) {
			t.Fatalf("%s request keys = %v, want %v", tc.method, got, want)
		}
	}
}

// fakeGoalStore is a scripted GoalStore: it records the call it received and
// answers what the test configured. The framework's own state machine is
// exercised through the real store in cli/consolergoals_test.go; this fake is
// here so the envelope mapping can be asserted without one.
type fakeGoalStore struct {
	calls       []string
	sessionID   string
	ref         GoalRef
	request     CreateGoalRequest
	edit        EditGoalRequest
	view        GoalView
	hasGoal     bool
	createValue CreateGoalResult
	clearValue  GoalRef
	err         error
}

func (s *fakeGoalStore) record(call, sessionID string) {
	s.calls = append(s.calls, call)
	s.sessionID = sessionID
}

func (s *fakeGoalStore) Goal(_ context.Context, sessionID string) (GoalView, bool, error) {
	s.record("get", sessionID)
	return s.view, s.hasGoal, s.err
}

func (s *fakeGoalStore) Create(_ context.Context, sessionID string, request CreateGoalRequest) (CreateGoalResult, error) {
	s.record("create", sessionID)
	s.request = request
	return s.createValue, s.err
}

func (s *fakeGoalStore) Edit(_ context.Context, sessionID string, ref GoalRef, request EditGoalRequest) (GoalView, error) {
	s.record("edit", sessionID)
	s.ref, s.edit = ref, request
	return s.view, s.err
}

func (s *fakeGoalStore) Pause(_ context.Context, sessionID string, ref GoalRef) (GoalView, error) {
	s.record("pause", sessionID)
	s.ref = ref
	return s.view, s.err
}

func (s *fakeGoalStore) Resume(_ context.Context, sessionID string, ref GoalRef) (GoalView, error) {
	s.record("resume", sessionID)
	s.ref = ref
	return s.view, s.err
}

func (s *fakeGoalStore) Complete(_ context.Context, sessionID string, ref GoalRef) (GoalView, error) {
	s.record("complete", sessionID)
	s.ref = ref
	return s.view, s.err
}

func (s *fakeGoalStore) Clear(_ context.Context, sessionID string, ref GoalRef) (GoalRef, error) {
	s.record("clear", sessionID)
	s.ref = ref
	return s.clearValue, s.err
}

// goalsFixture builds a fixture with a scripted store and one pending session,
// which is a session the goal methods accept: a goal is set before the first
// prompt as often as after it.
func goalsFixture(t *testing.T) (*fixture, *fakeGoalStore, string) {
	t.Helper()
	f := newFixture(t, Config{})
	store := &fakeGoalStore{}
	f.handler.SetGoals(store)
	return f, store, f.createSession(t)
}

func (f *fixture) goalCall(t *testing.T, method, args string) *httptest.ResponseRecorder {
	t.Helper()
	return f.post(t, "/api/goals/"+method, rpcBody(t, "rpc-goal", "goals/"+method, args))
}

// A read with no goal answers `{ok:true}` with no `value` key at all, which is
// what the client's `goal === undefined` test needs. A JSON null would arrive as
// a value and the dock would read `.id` off it.
func TestGoalsGetOmitsTheValueWithoutAGoal(t *testing.T) {
	f, store, sessionID := goalsFixture(t)
	store.hasGoal = false

	recorder := f.goalCall(t, "get", fmt.Sprintf(`{"agentId":%s}`, mustJSON(t, sessionID)))
	envelope := decodeResponse(t, recorder)
	if !envelope.Result.OK {
		t.Fatalf("read failed: %s", recorder.Body.String())
	}
	if bytes.Contains(recorder.Body.Bytes(), []byte(`"value"`)) {
		t.Fatalf("a session with no goal carried a value: %s", recorder.Body.String())
	}
	if store.calls[0] != "get" {
		t.Fatalf("calls = %v, want a get", store.calls)
	}
}

// A read with a goal answers the whole view, including the derived activation.
func TestGoalsGetAnswersTheView(t *testing.T) {
	f, store, sessionID := goalsFixture(t)
	store.hasGoal = true
	store.view = GoalView{ID: "goal-1", Revision: 3, Objective: "ship it", Phase: "active",
		MaxGoalRounds: 8, RoundsStarted: 2, CreatedAt: 10, UpdatedAt: 20, Activation: "armed"}

	recorder := f.goalCall(t, "get", fmt.Sprintf(`{"agentId":%s}`, mustJSON(t, sessionID)))
	var view GoalView
	decodeValue(t, recorder, &view)
	if view.ID != "goal-1" || view.Activation != "armed" || view.Revision != 3 {
		t.Fatalf("view = %+v, want the stored goal", view)
	}
}

// The console's own call shape names the request object, so the host has to
// accept the flattened splice as well as the flat form a hand-written call uses.
func TestGoalsCreateAcceptsTheRequestSplice(t *testing.T) {
	f, store, sessionID := goalsFixture(t)
	store.createValue = CreateGoalResult{Ref: GoalRef{ID: "goal-7", Revision: 1}}

	recorder := f.goalCall(t, "create", fmt.Sprintf(
		`{"agentId":%s,"request":{"objective":"ship it","maxGoalRounds":4}}`, mustJSON(t, sessionID)))
	var created CreateGoalResult
	decodeValue(t, recorder, &created)
	if created.Ref.ID != "goal-7" || created.Ref.Revision != 1 {
		t.Fatalf("created = %+v, want the host's ref", created)
	}
	if store.request.Objective != "ship it" || store.request.MaxGoalRounds != 4 {
		t.Fatalf("create request = %+v, want the spliced fields", store.request)
	}
}

// The flat form is what the goal command and the curl evidence use.
func TestGoalsCreateAcceptsFlatArguments(t *testing.T) {
	f, store, sessionID := goalsFixture(t)
	store.createValue = CreateGoalResult{Ref: GoalRef{ID: "goal-8", Revision: 1}}

	recorder := f.goalCall(t, "create", fmt.Sprintf(
		`{"agentId":%s,"objective":"ship it"}`, mustJSON(t, sessionID)))
	if envelope := decodeResponse(t, recorder); !envelope.Result.OK {
		t.Fatalf("create failed: %s", recorder.Body.String())
	}
	if store.request.Objective != "ship it" || store.request.MaxGoalRounds != 0 {
		t.Fatalf("create request = %+v, want the objective and no cap", store.request)
	}
}

func TestGoalsCreateRefusesAnEmptyObjective(t *testing.T) {
	f, store, sessionID := goalsFixture(t)
	recorder := f.goalCall(t, "create", fmt.Sprintf(`{"agentId":%s,"objective":"   "}`, mustJSON(t, sessionID)))
	assertMethodFailure(t, recorder, GoalCodeInvalidObjective)
	if len(store.calls) != 0 {
		t.Fatalf("calls = %v, want the store untouched", store.calls)
	}
}

func TestGoalsCreateRefusesANonPositiveCap(t *testing.T) {
	f, store, sessionID := goalsFixture(t)
	recorder := f.goalCall(t, "create", fmt.Sprintf(
		`{"agentId":%s,"objective":"ship it","maxGoalRounds":0}`, mustJSON(t, sessionID)))
	// The reference's resolveMaxGoalRounds owns this code, so it is served rather
	// than the gateway's generic argument failure.
	assertMethodFailure(t, recorder, GoalCodeInvalidMaxRounds)
	if len(store.calls) != 0 {
		t.Fatalf("calls = %v, want the store untouched", store.calls)
	}
}

// An edit that changes nothing is the reference's GOAL_INVALID_EDIT rather than
// a no-op that answers a successful view.
func TestGoalsEditRequiresAChange(t *testing.T) {
	f, store, sessionID := goalsFixture(t)
	recorder := f.goalCall(t, "edit", fmt.Sprintf(
		`{"agentId":%s,"ref":{"id":"goal-1","revision":1}}`, mustJSON(t, sessionID)))
	assertMethodFailure(t, recorder, GoalCodeInvalidEdit)
	if len(store.calls) != 0 {
		t.Fatalf("calls = %v, want the store untouched", store.calls)
	}
}

// An edit that changes only the cap passes a nil objective, so the store can
// tell "change nothing here" from "clear this field".
func TestGoalsEditSendsOnlyTheFieldsPresent(t *testing.T) {
	f, store, sessionID := goalsFixture(t)
	store.view = GoalView{ID: "goal-1", Revision: 2, Objective: "ship it", Phase: "active", Activation: "armed"}

	recorder := f.goalCall(t, "edit", fmt.Sprintf(
		`{"agentId":%s,"ref":{"id":"goal-1","revision":1},"maxGoalRounds":6}`, mustJSON(t, sessionID)))
	if envelope := decodeResponse(t, recorder); !envelope.Result.OK {
		t.Fatalf("edit failed: %s", recorder.Body.String())
	}
	if store.ref != (GoalRef{ID: "goal-1", Revision: 1}) {
		t.Fatalf("ref = %+v, want the compare-and-set identity", store.ref)
	}
	if store.edit.Objective != nil {
		t.Fatalf("objective = %v, want nil for an untouched field", *store.edit.Objective)
	}
	if store.edit.MaxGoalRounds == nil || *store.edit.MaxGoalRounds != 6 {
		t.Fatalf("maxGoalRounds = %v, want 6", store.edit.MaxGoalRounds)
	}
}

// The ref is the mutation's compare-and-set identity, so every malformed ref is
// refused before the store sees it.
func TestGoalsRefIsValidated(t *testing.T) {
	f, store, sessionID := goalsFixture(t)
	for name, ref := range map[string]string{
		"absent":        ``,
		"not an object": `"goal-1"`,
		"no id":         `{"revision":1}`,
		"empty id":      `{"id":"  ","revision":1}`,
		"no revision":   `{"id":"goal-1"}`,
		"zero revision": `{"id":"goal-1","revision":0}`,
		"unknown field": `{"id":"goal-1","revision":1,"phase":"active"}`,
	} {
		t.Run(name, func(t *testing.T) {
			args := fmt.Sprintf(`{"agentId":%s`, mustJSON(t, sessionID))
			if ref != `` {
				args += `,"ref":` + ref
			}
			args += `}`
			recorder := f.goalCall(t, "pause", args)
			assertMethodFailure(t, recorder, codeArgumentsInvalid)
		})
	}
	if len(store.calls) != 0 {
		t.Fatalf("calls = %v, want every malformed ref refused before the store", store.calls)
	}
}

// Every method is scoped by a session this host serves. A session it does not
// serve is GOAL_AGENT_NOT_LIVE, not an empty goal: the difference between "no
// goal" and "no such session" is one an operator can act on.
func TestGoalsRefuseAnUnknownSession(t *testing.T) {
	f, store, _ := goalsFixture(t)
	const unknown = "run-does-not-exist-0000"
	ref := `"ref":{"id":"goal-1","revision":1}`
	for _, method := range []string{"get", "create", "edit", "pause", "resume", "complete", "clear"} {
		t.Run(method, func(t *testing.T) {
			payload := fmt.Sprintf(`{"agentId":%s}`, mustJSON(t, unknown))
			switch method {
			case "create":
				payload = fmt.Sprintf(`{"agentId":%s,"objective":"ship it"}`, mustJSON(t, unknown))
			case "edit":
				payload = fmt.Sprintf(`{"agentId":%s,%s,"objective":"ship it"}`, mustJSON(t, unknown), ref)
			case "get":
			default:
				payload = fmt.Sprintf(`{"agentId":%s,%s}`, mustJSON(t, unknown), ref)
			}
			recorder := f.goalCall(t, method, payload)
			envelope := assertMethodFailure(t, recorder, GoalCodeAgentNotLive)
			if envelope.Result.Error.Details["sessionId"] != "run-does-not-exist-0000" {
				t.Fatalf("details = %v, want the refused session", envelope.Result.Error.Details)
			}
		})
	}
	if len(store.calls) != 0 {
		t.Fatalf("calls = %v, want the store untouched", store.calls)
	}
}

// A store refusal keeps the goal domain's own code and names the session, which
// is what the console renders next to the message.
func TestGoalsKeepTheStoresOwnRefusalCode(t *testing.T) {
	f, store, sessionID := goalsFixture(t)
	store.err = &GoalError{Code: GoalCodeStaleRevision, Message: "goal goal-1 is at revision 2, not 1"}

	recorder := f.goalCall(t, "pause", fmt.Sprintf(
		`{"agentId":%s,"ref":{"id":"goal-1","revision":1}}`, mustJSON(t, sessionID)))
	envelope := assertMethodFailure(t, recorder, GoalCodeStaleRevision)
	if envelope.Result.Error.Details["sessionId"] != sessionID {
		t.Fatalf("details = %v, want the session named", envelope.Result.Error.Details)
	}
}

// An error that is not a GoalError is this host's own failure, not a fact about
// the goal: it must not be served under one of the domain's codes.
func TestGoalsReportAnUnknownStoreFailureAsInternal(t *testing.T) {
	f, store, sessionID := goalsFixture(t)
	store.err = fmt.Errorf("disk is on fire")

	recorder := f.goalCall(t, "resume", fmt.Sprintf(
		`{"agentId":%s,"ref":{"id":"goal-1","revision":1}}`, mustJSON(t, sessionID)))
	assertMethodFailure(t, recorder, codeInternal)
}

// Each ref-only method reaches its own store operation, and answers the view.
func TestGoalsTransitionsReachTheStore(t *testing.T) {
	f, store, sessionID := goalsFixture(t)
	store.view = GoalView{ID: "goal-1", Revision: 4, Objective: "ship it", Phase: "paused", Activation: "disarmed"}
	for _, method := range []string{"pause", "resume", "complete"} {
		t.Run(method, func(t *testing.T) {
			recorder := f.goalCall(t, method, fmt.Sprintf(
				`{"agentId":%s,"ref":{"id":"goal-1","revision":3}}`, mustJSON(t, sessionID)))
			var view GoalView
			decodeValue(t, recorder, &view)
			if view.Revision != 4 {
				t.Fatalf("view = %+v, want the mutated revision", view)
			}
			if len(store.calls) == 0 || store.calls[len(store.calls)-1] != method {
				t.Fatalf("calls = %v, want %s last", store.calls, method)
			}
		})
	}
}

// A clear answers the ref it removed, which is how a client tells which revision
// it deleted.
func TestGoalsClearAnswersTheClearedRef(t *testing.T) {
	f, store, sessionID := goalsFixture(t)
	store.clearValue = GoalRef{ID: "goal-1", Revision: 5}

	recorder := f.goalCall(t, "clear", fmt.Sprintf(
		`{"agentId":%s,"ref":{"id":"goal-1","revision":5}}`, mustJSON(t, sessionID)))
	var cleared GoalRef
	decodeValue(t, recorder, &cleared)
	if cleared != (GoalRef{ID: "goal-1", Revision: 5}) {
		t.Fatalf("cleared = %+v, want the ref it removed", cleared)
	}
}

// A host with no goal store answers the namespace as unconfigured rather than as
// an empty goal, and names the dependency so the operator knows what to install.
func TestGoalsReportTheMissingStore(t *testing.T) {
	f := newFixture(t, Config{})
	sessionID := f.createSession(t)
	recorder := f.goalCall(t, "get", fmt.Sprintf(`{"agentId":%s}`, mustJSON(t, sessionID)))
	envelope := assertMethodFailure(t, recorder, codeUnimplemented)
	if envelope.Result.Error.Details["dependency"] != "GoalStore" {
		t.Fatalf("details = %v, want the missing dependency named", envelope.Result.Error.Details)
	}
}

// An unknown argument is refused rather than ignored: a mistyped argument is a
// call aimed at something the caller did not mean.
func TestGoalsRejectUnknownArguments(t *testing.T) {
	f, store, sessionID := goalsFixture(t)
	recorder := f.goalCall(t, "get", fmt.Sprintf(`{"agentId":%s,"phase":"active"}`, mustJSON(t, sessionID)))
	assertMethodFailure(t, recorder, codeArgumentsInvalid)
	if len(store.calls) != 0 {
		t.Fatalf("calls = %v, want the store untouched", store.calls)
	}
}

// readSource reads one file this test compares against.
func readSource(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(content)
}

// goalDescriptor returns the slice of the generated remote map that declares one
// goal method, from its id to the next descriptor.
func goalDescriptor(t *testing.T, source, method string) string {
	t.Helper()
	marker := fmt.Sprintf(`id: "@deepseek-ai/dsh-goal#goals/%s"`, method)
	start := strings.Index(source, marker)
	if start < 0 {
		t.Fatalf("the console declares no goals/%s", method)
	}
	rest := source[start+len(marker):]
	// The next descriptor is the end of this one, whoever declares it: the goal
	// package is not last in the generated map.
	if end := strings.Index(rest, `id: "`); end >= 0 {
		return rest[:end]
	}
	return rest
}

// goalWireNames reads the parameter wire names of one descriptor, in order.
func goalWireNames(t *testing.T, descriptor, method string) []string {
	t.Helper()
	// The scope block names the agent wire once more than the parameter list
	// does; dropping it leaves exactly the parameters the client sends.
	descriptor = regexp.MustCompile(`(?s)scope: \{[^}]*\}`).ReplaceAllString(descriptor, "")
	pattern := regexp.MustCompile(`wire: "([^"]+)"`)
	matches := pattern.FindAllStringSubmatch(descriptor, -1)
	if len(matches) == 0 {
		t.Fatalf("goals/%s declares no parameter wire names", method)
	}
	names := make([]string, 0, len(matches))
	for _, match := range matches {
		names = append(names, match[1])
	}
	return names
}

// goalSchemaExpression returns the generated schema expression for one method's
// parameter or result, e.g. `parameter_1`.
func goalSchemaExpression(t *testing.T, source, method, name string) string {
	t.Helper()
	marker := fmt.Sprintf("const _deepseek_ai_dsh_goal_goals_%s_%s$schema = () =>", method, name)
	start := strings.Index(source, marker)
	if start < 0 {
		t.Fatalf("the console declares no schema %s for goals/%s", name, method)
	}
	rest := source[start:]
	assign := strings.Index(rest, "??=")
	if assign < 0 {
		t.Fatalf("schema %s for goals/%s has no assignment", name, method)
	}
	depth := 0
	for i := assign; i < len(rest); i++ {
		switch rest[i] {
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			depth--
		case ';':
			if depth == 0 {
				return rest[assign:i]
			}
		}
	}
	t.Fatalf("schema %s for goals/%s has no terminator", name, method)
	return ""
}

// goalObjectParameters returns the top-level keys of every object literal
// declared in the parameter schemas of one method, in declaration order. A
// parameter that is not an object (the scoped session id) contributes nothing.
func goalObjectParameters(t *testing.T, source, method string) [][]string {
	t.Helper()
	var objects [][]string
	for index := 0; index < 8; index++ {
		name := fmt.Sprintf("parameter_%d", index)
		marker := fmt.Sprintf("const _deepseek_ai_dsh_goal_goals_%s_%s$schema = () =>", method, name)
		if !strings.Contains(source, marker) {
			break
		}
		expression := goalSchemaExpression(t, source, method, name)
		if keys := topLevelKeys(t, expression); len(keys) > 0 {
			objects = append(objects, keys)
		}
	}
	return objects
}

// topLevelKeys returns the keys of the first object literal in a generated
// schema expression. Nested objects (a blocked reason) and string literals used
// as enum values are skipped: only keys directly inside the root object count.
func topLevelKeys(t *testing.T, expression string) []string {
	t.Helper()
	start := strings.Index(expression, "object({")
	if start < 0 {
		return nil
	}
	var keys []string
	depth := 0
	for i := start + len("object("); i < len(expression); i++ {
		switch expression[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return keys
			}
		case '"':
			end := strings.IndexByte(expression[i+1:], '"')
			if end < 0 {
				return keys
			}
			if depth == 1 {
				rest := strings.TrimLeft(expression[i+1+end+1:], " \n\t")
				if strings.HasPrefix(rest, ":") {
					keys = append(keys, expression[i+1:i+1+end])
				}
			}
			i += end + 1
		}
	}
	return keys
}

// handlerArgumentNames reads the argument names one handler function accepts, out
// of this repository's own source. `goalsTransition` is the shared body of the
// three ref-only transitions, so those three methods name its list.
func handlerArgumentNames(t *testing.T, source, function string) []string {
	t.Helper()
	marker := fmt.Sprintf("func (h *Handler) %s(", function)
	start := strings.Index(source, marker)
	if start < 0 {
		t.Fatalf("goals.go declares no %s", function)
	}
	rest := source[start:]
	if function == "goalsPause" || function == "goalsResume" || function == "goalsComplete" {
		if shared := strings.Index(rest, "func (h *Handler) goalsTransition("); shared >= 0 {
			rest = rest[shared:]
		}
	}
	call := strings.Index(rest, "rejectUnknownArguments(args, ")
	if call < 0 {
		t.Fatalf("%s does not reject unknown arguments", function)
	}
	rest = rest[call+len("rejectUnknownArguments(args, "):]
	end := strings.Index(rest, ")")
	if end < 0 {
		t.Fatalf("%s has an unterminated argument list", function)
	}
	names := []string{}
	for _, quoted := range regexp.MustCompile(`"([^"]+)"`).FindAllStringSubmatch(rest[:end], -1) {
		names = append(names, quoted[1])
	}
	if len(names) == 0 {
		t.Fatalf("%s accepts no named arguments", function)
	}
	return names
}

// jsonKeys returns the top-level keys of one value's JSON encoding.
func jsonKeys(t *testing.T, value any) []string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal %T: %v", value, err)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &object); err != nil {
		t.Fatalf("decode %s: %v", encoded, err)
	}
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	return keys
}

func sortedKeys(keys []string) []string {
	sorted := append([]string{}, keys...)
	sort.Strings(sorted)
	return sorted
}

func sameStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	left, right := sortedKeys(got), sortedKeys(want)
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func removeString(values []string, drop string) []string {
	kept := make([]string, 0, len(values))
	for _, value := range values {
		if value != drop {
			kept = append(kept, value)
		}
	}
	return kept
}

func stringPointer(value string) *string { return &value }

func intPointer(value int) *int { return &value }
