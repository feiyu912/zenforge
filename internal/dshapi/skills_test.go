package dshapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"regexp"
	"testing"
)

// skillSourceStub is a console skill source whose read can be made to fail, so a
// test can pin what the console is told when the catalog is unreadable.
type skillSourceStub struct {
	rows []SkillInfo
	err  error
}

func (s skillSourceStub) Skills(context.Context) ([]SkillInfo, error) {
	return s.rows, s.err
}

// The panel's rows are the catalog's, filtered to what an operator may invoke and
// carrying the skill's own guidance: the reference sends exactly these four
// fields, and its own row schema allows no more.
func TestSessionSkillsListsTheOperatorsCatalog(t *testing.T) {
	f := newFixture(t, Config{})
	f.handler.SetSkills(skillSourceStub{rows: []SkillInfo{
		{Name: "review", Description: "Review a change", WhenToUse: "when a diff needs a second pair of eyes", ModelInvocable: true},
		{Name: "operator-only", Description: "A private checklist", ModelInvocable: false},
	}})
	sessionID := f.createSession(t)

	recorder := f.post(t, "/api/skills/list",
		rpcBody(t, "rpc-skills", "skills/list", fmt.Sprintf(`{"sessionId":%s}`, mustJSON(t, sessionID))))
	if !decodeResponse(t, recorder).Result.OK {
		t.Fatalf("skills/list failed: %s", recorder.Body.String())
	}
	var value struct {
		Skills []SkillInfo `json:"skills"`
	}
	decodeValue(t, recorder, &value)
	if len(value.Skills) != 2 {
		t.Fatalf("skills = %+v, want two rows", value.Skills)
	}
	if value.Skills[0].Name != "review" || value.Skills[0].WhenToUse != "when a diff needs a second pair of eyes" {
		t.Fatalf("first row = %+v, want the guidance the skill declares", value.Skills[0])
	}
	if value.Skills[1].ModelInvocable {
		t.Fatalf("second row = %+v, want the model-hidden skill listed with its badge", value.Skills[1])
	}

	// A host with no skills installed answers an empty panel: an empty list is a
	// fact, and a missing key would read as a failure to the client.
	f.handler.SetSkills(skillSourceStub{})
	recorder = f.post(t, "/api/skills/list",
		rpcBody(t, "rpc-empty", "skills/list", fmt.Sprintf(`{"sessionId":%s}`, mustJSON(t, sessionID))))
	var empty struct {
		Skills []SkillInfo `json:"skills"`
	}
	decodeValue(t, recorder, &empty)
	if empty.Skills == nil || len(empty.Skills) != 0 {
		t.Fatalf("skills = %#v, want a present, empty array", empty.Skills)
	}
	if got := decodeValueMap(t, recorder); got == "" {
		t.Fatal("the empty answer carried no body")
	}
}

// The session is validated before the catalog is read: the panel belongs to a
// conversation, and an id this host does not know is the reference's own
// not-found rather than an empty panel for a session that does not exist.
func TestSessionSkillsValidatesTheSessionAndTheArguments(t *testing.T) {
	f := newFixture(t, Config{})
	f.handler.SetSkills(skillSourceStub{rows: []SkillInfo{{Name: "review", Description: "Review"}}})

	assertSkillsRefusal(t, f.post(t, "/api/skills/list",
		rpcBody(t, "rpc-missing", "skills/list", `{"sessionId":"run-nobody"}`)),
		codeSessionNotFound, `session "run-nobody" not found`, map[string]any{"sessionId": "run-nobody"})
	assertSkillsRefusal(t, f.post(t, "/api/skills/list",
		rpcBody(t, "rpc-blank", "skills/list", `{}`)),
		codeArgumentsInvalid, `argument "sessionId" is required`, map[string]any{"argument": "sessionId"})
	assertSkillsRefusal(t, f.post(t, "/api/skills/list",
		rpcBody(t, "rpc-extra", "skills/list", `{"sessionId":"run-1","cwd":"/tmp"}`)),
		codeArgumentsInvalid, `unexpected argument "cwd"`, map[string]any{"argument": "cwd"})
}

// A catalog that cannot be read, and a host that was started without one, are
// different answers: the first names the failure the reference names, the second
// names the seam serve installs.
func TestSessionSkillsReportsAMissingOrBrokenCatalog(t *testing.T) {
	f := newFixture(t, Config{})
	sessionID := f.createSession(t)

	assertSkillsRefusal(t, f.post(t, "/api/skills/list",
		rpcBody(t, "rpc-absent", "skills/list", fmt.Sprintf(`{"sessionId":%s}`, mustJSON(t, sessionID)))),
		codeUnimplemented,
		"skills/list is not configured: the host has no skill catalog; the serve command must install one with Handler.SetSkills",
		map[string]any{"dependency": "SkillSource"})

	f.handler.SetSkills(skillSourceStub{err: errors.New("stat skill root: permission denied")})
	assertSkillsRefusal(t, f.post(t, "/api/skills/list",
		rpcBody(t, "rpc-broken", "skills/list", fmt.Sprintf(`{"sessionId":%s}`, mustJSON(t, sessionID)))),
		codeInternal, "skill listing failed: stat skill root: permission denied", map[string]any{})
}

// skillsBundle reads the same vendored session-controller bundle under the skills
// namespace, which is where the panel's method is declared.
func skillsBundle(t *testing.T) vendoredBundle {
	t.Helper()
	return vendoredBundle{source: readSource(t, sessionRemotePath), pkg: "@deepseek-ai/dsh-api-session-controller", ns: "skills"}
}

// The request's one key, the result's two, and the three keys of a row are the
// vendored console's, re-derived from the bundle rather than restated: a row the
// client cannot render is a regression the schema catches.
func TestSkillsListEnvelopeMatchesTheVendoredConsole(t *testing.T) {
	console := skillsBundle(t)
	descriptor := console.descriptor(t, "list")
	if wires := console.wireNames(t, descriptor, "list"); !sameStrings(wires, []string{"request"}) {
		t.Fatalf("skills/list wires = %v, want the request object", wires)
	}
	objects := console.objectParameters(t, "list")
	if len(objects) != 1 {
		t.Fatalf("skills/list object parameters = %v, want one", objects)
	}
	if keys := sortedKeys(objects[0]); !sameStrings(keys, []string{"sessionId"}) {
		t.Fatalf("skills/list request keys = %v, want the session", keys)
	}
	result := console.schemaExpression(t, "list", "result")
	if keys := sortedKeys(topLevelKeys(t, result)); !sameStrings(keys, []string{"skills"}) {
		t.Fatalf("skills/list result keys = %v, want the rows", keys)
	}
	// Each row is the console's own: name, description, optional guidance, and the
	// model's invocability.
	rowKeys := regexp.MustCompile(`"([A-Za-z][A-Za-z0-9]*)":`).FindAllStringSubmatch(result, -1)
	names := make([]string, 0, len(rowKeys))
	for _, match := range rowKeys {
		names = append(names, match[1])
	}
	for _, want := range []string{"path", "name", "description", "whenToUse", "modelInvocable"} {
		if !hasString(names, want) {
			t.Fatalf("row schema = %v, want %q among the row's keys", names, want)
		}
	}
	// This host populates the four the reference does; the fifth is optional there
	// and is left out here rather than invented.
	encoded, err := json.Marshal(SkillInfo{Name: "review", Description: "d", ModelInvocable: true})
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]any
	if err := json.Unmarshal(encoded, &object); err != nil {
		t.Fatal(err)
	}
	if _, present := object["whenToUse"]; present {
		t.Fatalf("an empty guidance was sent: %s", encoded)
	}
	if keys := sortedKeys(keysOf(object)); !sameStrings(keys, []string{"description", "modelInvocable", "name"}) {
		t.Fatalf("row keys = %v, want the three a guidance-free skill carries", keys)
	}
}

// keysOf names the fields of a decoded row object.
func keysOf(object map[string]any) []string {
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	return keys
}

// hasString reports whether a decoded list of names contains one.
func hasString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// assertSkillsRefusal pins one refusal: its code, the sentence the console shows
// and the details it reads out of the error.
func assertSkillsRefusal(t *testing.T, recorder *httptest.ResponseRecorder, code, message string, details map[string]any) {
	t.Helper()
	envelope := assertMethodFailure(t, recorder, code)
	if envelope.Result.Error.Message != message {
		t.Fatalf("message = %q, want %q", envelope.Result.Error.Message, message)
	}
	for key, want := range details {
		if got := envelope.Result.Error.Details[key]; got != want {
			t.Fatalf("details[%s] = %v, want %v", key, got, want)
		}
	}
}
