package workflow

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestValidateObjectSchemaAcceptsTheSupportedSubset(t *testing.T) {
	schemas := map[string]string{
		"minimal object":   `{"type":"object"}`,
		"annotated":        `{"type":"object","title":"Verdict","description":"a verdict","default":{},"examples":[{"ok":true}]}`,
		"properties":       `{"type":"object","properties":{"ok":{"type":"boolean"},"score":{"type":"number"}},"required":["ok"],"additionalProperties":false}`,
		"nested array":     `{"type":"object","properties":{"items":{"type":"array","items":{"type":"object","properties":{"name":{"type":"string"}}}}}}`,
		"enum":             `{"type":"object","properties":{"verdict":{"type":"string","enum":["ok","bad"]}}}`,
		"const in enum":    `{"type":"object","properties":{"kind":{"type":"string","enum":["a","b"],"const":"a"}}}`,
		"integer":          `{"type":"object","properties":{"count":{"type":"integer","const":3}}}`,
		"null":             `{"type":"object","properties":{"nothing":{"type":"null"}}}`,
		"oneOf":            `{"type":"object","properties":{"value":{"oneOf":[{"type":"string"},{"type":"integer"}]}}}`,
		"unspecified leaf": `{"type":"object","properties":{"anything":{}}}`,
	}
	for name, schema := range schemas {
		t.Run(name, func(t *testing.T) {
			if err := ValidateObjectSchema(json.RawMessage(schema)); err != nil {
				t.Fatalf("ValidateObjectSchema(%s) = %v", schema, err)
			}
		})
	}
}

func TestValidateObjectSchemaRefusesTheUnsupportedSubset(t *testing.T) {
	cases := map[string]struct {
		schema string
		want   string
	}{
		"unknown keyword":       {`{"type":"object","properties":{"a":{"type":"string","pattern":"^a"}}}`, "pattern"},
		"type array":            {`{"type":"object","properties":{"a":{"type":["string","null"]}}}`, "single type string"},
		"unknown type":          {`{"type":"object","properties":{"a":{"type":"date"}}}`, "must be one of"},
		"root not an object":    {`{"type":"array","items":{"type":"string"}}`, `schema.type must be "object"`},
		"no type at all":        {`{"properties":{"a":{"type":"string"}}}`, "requires type or oneOf"},
		"type beside oneOf":     {`{"type":"object","properties":{"a":{"type":"string","oneOf":[{"type":"string"},{"type":"null"}]}}}`, "cannot declare both"},
		"oneOf sibling":         {`{"type":"object","properties":{"a":{"oneOf":[{"type":"string"},{"type":"null"}],"enum":["a"]}}}`, "not supported beside oneOf"},
		"short oneOf":           {`{"type":"object","properties":{"a":{"oneOf":[{"type":"string"}]}}}`, "at least two schemas"},
		"required not declared": {`{"type":"object","properties":{"a":{"type":"string"}},"required":["b"]}`, `required names "b"`},
		"required not strings":  {`{"type":"object","required":[1]}`, "array of strings"},
		"additional non-bool":   {`{"type":"object","additionalProperties":{"type":"string"}}`, "must be a boolean"},
		"items on object":       {`{"type":"object","items":{"type":"string"}}`, `items is not supported on type "object"`},
		"properties on array":   {`{"type":"object","properties":{"a":{"type":"array","properties":{}}}}`, `properties is not supported on type "array"`},
		"enum on object":        {`{"type":"object","properties":{"a":{"type":"object","enum":["x"]}}}`, `enum is not supported on type "object"`},
		"empty enum":            {`{"type":"object","properties":{"a":{"type":"string","enum":[]}}}`, "non-empty array"},
		"enum type mismatch":    {`{"type":"object","properties":{"a":{"type":"string","enum":[1]}}}`, "non-empty array of string values"},
		"const type mismatch":   {`{"type":"object","properties":{"a":{"type":"integer","const":"3"}}}`, "must be a integer value"},
		"const outside enum":    {`{"type":"object","properties":{"a":{"type":"string","enum":["a"],"const":"b"}}}`, "must be one of"},
		"properties not object": {`{"type":"object","properties":[]}`, "must be an object of schemas"},
		"not JSON":              {`{"type":`, "not valid JSON"},
		"empty":                 {``, "schema is required"},
		"annotation type":       {`{"type":"object","title":7}`, "must be a string"},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			err := ValidateObjectSchema(json.RawMessage(testCase.schema))
			if err == nil {
				t.Fatalf("ValidateObjectSchema(%s) was accepted", testCase.schema)
			}
			code, ok := ErrorCodeOf(err)
			if !ok || code != CodeUnsupportedSchema {
				t.Fatalf("error = %v (code %s)", err, code)
			}
			if !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("error = %q, want it to mention %q", err, testCase.want)
			}
		})
	}
}

func TestValidateObjectSchemaReportsEveryViolation(t *testing.T) {
	err := ValidateObjectSchema(json.RawMessage(
		`{"type":"object","properties":{"a":{"type":"string","pattern":"^a","maxLength":3},"b":{"type":"nope"}},"required":["c"]}`,
	))
	if err == nil {
		t.Fatal("the schema was accepted")
	}
	message := err.Error()
	for _, want := range []string{"pattern", "maxLength", "must be one of", `required names "c"`} {
		if !strings.Contains(message, want) {
			t.Fatalf("message = %q, want it to mention %q", message, want)
		}
	}
}
