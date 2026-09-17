package model

import "errors"

// ErrUnsupportedOutputSchema reports that a model adapter cannot enforce
// a JSON Schema on the final response. Adapters that cannot enforce one
// fail loudly instead of silently ignoring the request.
var ErrUnsupportedOutputSchema = errors.New("model adapter does not support an output schema")

// OutputSchemaLabel returns the provider-facing schema name, applying
// the default when the caller supplied none.
func OutputSchemaLabel(req Request) string {
	if req.OutputSchemaName != "" {
		return req.OutputSchemaName
	}
	return DefaultOutputSchemaName
}

// HasOutputSchema reports whether the request constrains its final
// response shape.
func HasOutputSchema(req Request) bool {
	return len(req.OutputSchema) > 0
}
