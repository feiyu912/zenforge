package jsonschema

import "testing"

func TestInferStructSchemaUsesJSONTagsAndRequiredFields(t *testing.T) {
	type input struct {
		Query string   `json:"query" jsonschema:"required,description=Search query"`
		Limit int      `json:"limit,omitempty"`
		Tags  []string `json:"tags,omitempty"`
		Skip  string   `json:"-"`
	}

	schema := Infer(input{})
	if schema["type"] != "object" {
		t.Fatalf("unexpected schema type: %#v", schema)
	}
	properties := schema["properties"].(map[string]any)
	if _, ok := properties["query"]; !ok {
		t.Fatalf("missing query property: %#v", properties)
	}
	if _, ok := properties["Skip"]; ok {
		t.Fatalf("json - field should be skipped: %#v", properties)
	}
	query := properties["query"].(map[string]any)
	if query["description"] != "Search query" {
		t.Fatalf("missing description: %#v", query)
	}
	required := schema["required"].([]string)
	if len(required) != 1 || required[0] != "query" {
		t.Fatalf("unexpected required fields: %#v", required)
	}
}
