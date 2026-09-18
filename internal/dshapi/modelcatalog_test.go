package dshapi

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

// fixtureCatalog is a well-formed catalog in the upstream shape: one provider
// group with one model that exposes reasoning efforts. The values are test
// fixtures, not any real host's configured model.
func fixtureCatalog() ModelCatalog {
	return ModelCatalog{
		Default:           ModelSelection{Provider: "fixture", Model: "fixture-model", ReasoningEffort: "high"},
		RoutableProviders: []string{"fixture"},
		Groups: []ModelProviderGroup{{
			ID:   "fixture",
			Name: "Fixture",
			Models: []ModelCatalogModel{{
				ID:   "fixture-model",
				Name: "Fixture Model",
				Reasoning: &ModelReasoning{
					Efforts:       []ModelReasoningEffort{{ID: "high", Name: "High"}},
					DefaultEffort: "high",
				},
			}},
		}},
		Failures: []ModelCatalogFailure{},
	}
}

func TestSessionModelCatalogWithoutSourceIsUnimplemented(t *testing.T) {
	f := newFixture(t, Config{})
	recorder := f.post(t, "/api/session/modelCatalog",
		rpcBody(t, "rpc-catalog", "session/modelCatalog", ""))
	envelope := assertMethodFailure(t, recorder, codeUnimplemented)
	if envelope.RPCID != "rpc-catalog" {
		t.Fatalf("rpcId = %q, want rpc-catalog", envelope.RPCID)
	}
	if !strings.Contains(envelope.Result.Error.Message, "model catalog source") {
		t.Fatalf("unimplemented message does not name what is missing: %q", envelope.Result.Error.Message)
	}
	if got := envelope.Result.Error.Details["dependency"]; got != "ModelCatalogSource" {
		t.Fatalf("details dependency = %v, want ModelCatalogSource", got)
	}
	// The ruled-out failure mode is a fabricated catalog, so the answer must
	// carry no value at all.
	if strings.Contains(recorder.Body.String(), `"value"`) {
		t.Fatalf("unimplemented answer carried a value: %s", recorder.Body.String())
	}
}

func TestSessionModelCatalogReturnsConfiguredCatalog(t *testing.T) {
	f := newFixture(t, Config{})
	want := fixtureCatalog()
	f.handler.SetModelCatalog(func() ModelCatalog { return want })
	recorder := f.post(t, "/api/session/modelCatalog",
		rpcBody(t, "rpc-catalog-echo", "session/modelCatalog", ""))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	envelope := decodeResponse(t, recorder)
	if envelope.Type != "server-response" {
		t.Fatalf("type = %q, want server-response", envelope.Type)
	}
	if envelope.RPCID != "rpc-catalog-echo" {
		t.Fatalf("rpcId = %q, want rpc-catalog-echo", envelope.RPCID)
	}
	// Pin the whole wire shape, not just the decoded struct: the console's
	// selector reads these exact keys, and an optional field must stay absent
	// rather than appear as an empty string or null.
	const wantJSON = `{"default":{"provider":"fixture","model":"fixture-model","reasoningEffort":"high"},` +
		`"routableProviders":["fixture"],` +
		`"groups":[{"id":"fixture","name":"Fixture","models":[{"id":"fixture-model","name":"Fixture Model",` +
		`"reasoning":{"efforts":[{"id":"high","name":"High"}],"defaultEffort":"high"}}]}],` +
		`"failures":[]}`
	if got := string(envelope.Result.Value); got != wantJSON {
		t.Fatalf("catalog wire shape =\n%s\nwant\n%s", got, wantJSON)
	}
	var decoded ModelCatalog
	if err := json.Unmarshal(envelope.Result.Value, &decoded); err != nil {
		t.Fatalf("decode catalog: %v", err)
	}
	if !reflect.DeepEqual(decoded, want) {
		t.Fatalf("decoded catalog = %#v, want %#v", decoded, want)
	}
}

func TestSessionModelCatalogRejectsArguments(t *testing.T) {
	f := newFixture(t, Config{})
	f.handler.SetModelCatalog(func() ModelCatalog { return fixtureCatalog() })
	cases := []struct {
		name string
		args string
		// reported is the alphabetically first extra field, which is the
		// deterministic one the error names.
		reported string
	}{
		{"single unexpected field", `{"model":"other"}`, "model"},
		{"several unexpected fields", `{"zeta":1,"alpha":2,"secret":"sk-live-DEADBEEF"}`, "alpha"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			recorder := f.post(t, "/api/session/modelCatalog",
				rpcBody(t, "rpc-catalog-args", "session/modelCatalog", testCase.args))
			envelope := assertMethodFailure(t, recorder, codeArgumentsInvalid)
			if got := envelope.Result.Error.Details["argument"]; got != testCase.reported {
				t.Fatalf("details argument = %v, want %q", got, testCase.reported)
			}
			if !strings.Contains(envelope.Result.Error.Message, testCase.reported) {
				t.Fatalf("argument error does not name %q: %q", testCase.reported, envelope.Result.Error.Message)
			}
			if strings.Contains(recorder.Body.String(), `"value"`) {
				t.Fatalf("argument error carried a value: %s", recorder.Body.String())
			}
		})
	}
}

func TestSessionModelCatalogSetNilRestoresUnimplemented(t *testing.T) {
	f := newFixture(t, Config{})
	f.handler.SetModelCatalog(func() ModelCatalog { return fixtureCatalog() })
	f.handler.SetModelCatalog(nil)
	recorder := f.post(t, "/api/session/modelCatalog",
		rpcBody(t, "rpc-catalog-cleared", "session/modelCatalog", ""))
	assertMethodFailure(t, recorder, codeUnimplemented)
}

func TestSessionModelCatalogWritesRequiredArrays(t *testing.T) {
	f := newFixture(t, Config{})
	// A source that leaves every array nil: the wire type declares them
	// required, so they must marshal as [] and never as null.
	f.handler.SetModelCatalog(func() ModelCatalog {
		return ModelCatalog{Default: ModelSelection{Provider: "fixture", Model: "fixture-model"}}
	})
	recorder := f.post(t, "/api/session/modelCatalog",
		rpcBody(t, "rpc-catalog-arrays", "session/modelCatalog", ""))
	body := recorder.Body.String()
	for _, want := range []string{`"routableProviders":[]`, `"groups":[]`, `"failures":[]`} {
		if !strings.Contains(body, want) {
			t.Fatalf("catalog body is missing %s: %s", want, body)
		}
	}
	if strings.Contains(body, "null") {
		t.Fatalf("a required array marshaled as null: %s", body)
	}
}
