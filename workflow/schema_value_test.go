package workflow

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func asError(err error, target **Error) bool {
	return errors.As(err, target)
}

// objectSchema is the shape the cases below vary one field at a time.
const objectSchema = `{
	"type": "object",
	"properties": {
		"verdict": {"type": "string", "enum": ["ok", "bad"]},
		"score": {"type": "integer"},
		"confidence": {"type": "number"},
		"notes": {"type": "array", "items": {"type": "string"}},
		"action": {"oneOf": [
			{"type": "object", "properties": {"kind": {"type": "string", "const": "none"}}, "required": ["kind"], "additionalProperties": false},
			{"type": "object", "properties": {"kind": {"type": "string", "const": "edit"}, "path": {"type": "string"}}, "required": ["kind", "path"], "additionalProperties": false}
		]}
	},
	"required": ["verdict", "score", "notes"],
	"additionalProperties": false
}`

func TestValidateObjectValueAcceptsConformingAnswers(t *testing.T) {
	cases := map[string]string{
		"the minimum":     `{"verdict": "ok", "score": 3, "notes": []}`,
		"every field":     `{"verdict": "bad", "score": 0, "confidence": 0.5, "notes": ["slow"], "action": {"kind": "edit", "path": "a.go"}}`,
		"number as 3.0":   `{"verdict": "ok", "score": 3.0, "notes": []}`,
		"null-free oneOf": `{"verdict": "ok", "score": 1, "notes": ["x"], "action": {"kind": "none"}}`,
	}
	for name, value := range cases {
		t.Run(name, func(t *testing.T) {
			if err := ValidateObjectValue(json.RawMessage(objectSchema), json.RawMessage(value)); err != nil {
				t.Fatalf("ValidateObjectValue returned error: %v", err)
			}
		})
	}
}

func TestValidateObjectValueReportsEveryViolation(t *testing.T) {
	cases := []struct {
		name  string
		value string
		want  []string
	}{
		{"a missing required field", `{"score": 1, "notes": []}`, []string{"value.verdict is required"}},
		{"a wrong type", `{"verdict": "ok", "score": 1, "notes": "none"}`, []string{"value.notes must be array"}},
		{"a non-integer number", `{"verdict": "ok", "score": 1.5, "notes": []}`, []string{"value.score must be integer"}},
		{"an enum outside the set", `{"verdict": "maybe", "score": 1, "notes": []}`, []string{"value.verdict is not one of the allowed enum values"}},
		{"an undeclared field", `{"verdict": "ok", "score": 1, "notes": [], "extra": 1}`, []string{"value.extra is not allowed (additionalProperties is false)"}},
		{"a bad item", `{"verdict": "ok", "score": 1, "notes": ["ok", 2]}`, []string{"value.notes[1] must be string"}},
		{"a oneOf with no match", `{"verdict": "ok", "score": 1, "notes": [], "action": {"kind": "edit"}}`, []string{"value.action must match exactly one oneOf branch (0 matched)"}},
		{"an object where the other branch fits", `{"verdict": "ok", "score": 1, "notes": [], "action": {"kind": "none", "path": "a.go"}}`, []string{"must match exactly one oneOf branch (0 matched)"}},
		{"a const violation", `{"verdict": "ok", "score": 1, "notes": [], "action": {"kind": "other"}}`, []string{"must match exactly one oneOf branch (0 matched)"}},
		{"an array root", `[1, 2]`, []string{"value must be object"}},
		{"several at once", `{"verdict": "maybe", "notes": 7}`, []string{
			"value.verdict is not one of the allowed enum values",
			"value.score is required",
			"value.notes must be array",
		}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			err := ValidateObjectValue(json.RawMessage(objectSchema), json.RawMessage(testCase.value))
			if err == nil {
				t.Fatal("ValidateObjectValue accepted a non-conforming answer")
			}
			message := err.Error()
			for _, want := range testCase.want {
				if !strings.Contains(message, want) {
					t.Fatalf("error = %q, want to contain %q", message, want)
				}
			}
			var coded *Error
			if !asError(err, &coded) || coded.Code != CodeAgentResult {
				t.Fatalf("error code = %#v", err)
			}
		})
	}
}

// TestValidateObjectValueReadsATypelessNodeByShape covers the exported
// function's behaviour for a hand-written schema, which the engine cannot
// produce: the subset check refuses a node without a type or oneOf.
func TestValidateObjectValueReadsATypelessNodeByShape(t *testing.T) {
	typeless := json.RawMessage(`{"type": "object", "properties": {"kind": {"enum": ["a", "b"]}}, "required": ["kind"]}`)
	if err := ValidateObjectValue(typeless, json.RawMessage(`{"kind": "a"}`)); err != nil {
		t.Fatalf("a typeless node with a matching enum was refused: %v", err)
	}
	if err := ValidateObjectValue(typeless, json.RawMessage(`{"kind": "c"}`)); err == nil ||
		!strings.Contains(err.Error(), "enum") {
		t.Fatalf("a typeless node did not enforce its enum: %v", err)
	}
	if err := ValidateObjectValue(typeless, json.RawMessage(`{"other": 1}`)); err == nil ||
		!strings.Contains(err.Error(), "value.kind is required") {
		t.Fatalf("a typeless node did not enforce required: %v", err)
	}
}

func TestValidateObjectValueWithoutASchemaAcceptsAnything(t *testing.T) {
	if err := ValidateObjectValue(nil, json.RawMessage(`{"anything": true}`)); err != nil {
		t.Fatalf("a call without a schema validated its answer: %v", err)
	}
}
