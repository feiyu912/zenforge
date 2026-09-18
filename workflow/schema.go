package workflow

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
)

// The enforced JSON Schema subset, matching the reference's validator: an
// object-rooted schema may use type, oneOf, properties, required,
// additionalProperties, items, enum and const, plus the description, title,
// default and examples annotations. Every other keyword is refused rather
// than ignored, because a script that asks for a constraint the child's model
// cannot be held to should hear about it instead of silently getting less.
var (
	schemaConstraintKeywords = map[string]bool{
		"type": true, "oneOf": true, "properties": true, "required": true,
		"additionalProperties": true, "items": true, "enum": true, "const": true,
	}
	schemaAnnotationKeywords = map[string]bool{
		"description": true, "title": true, "default": true, "examples": true,
	}
	schemaTypes = map[string]bool{
		"object": true, "array": true, "string": true,
		"number": true, "integer": true, "boolean": true, "null": true,
	}
	oneOfSiblingKeywords = []string{
		"properties", "required", "additionalProperties", "items", "enum", "const",
	}
	schemaKeywordTypes = map[string][]string{
		"properties":           {"object"},
		"required":             {"object"},
		"additionalProperties": {"object"},
		"items":                {"array"},
		"enum":                 {"string", "number", "integer", "boolean", "null"},
		"const":                {"string", "number", "integer", "boolean", "null"},
	}
)

// ValidateObjectSchema reports whether raw is inside the subset this package
// enforces for a child's structured output, and must be object-rooted. It
// returns every violation it found, so a script author can fix the schema in
// one pass instead of one error at a time.
func ValidateObjectSchema(raw json.RawMessage) error {
	if len(raw) == 0 {
		return newError(CodeUnsupportedSchema, "schema is required")
	}
	var decoded any
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if err := decoder.Decode(&decoded); err != nil {
		return newError(CodeUnsupportedSchema, "schema is not valid JSON: %v", err)
	}
	violations := make([]string, 0)
	checkSchemaNode(decoded, "schema", &violations)
	if len(violations) == 0 {
		root, ok := decoded.(map[string]any)
		if !ok {
			violations = append(violations, "schema must be an object")
		} else if root["type"] != "object" {
			violations = append(violations, `schema.type must be "object" (structured output is object-rooted)`)
		}
	}
	if len(violations) > 0 {
		return newError(CodeUnsupportedSchema, "schema is outside the supported subset — %s", strings.Join(violations, "; "))
	}
	return nil
}

func checkSchemaNode(node any, path string, violations *[]string) {
	record, ok := node.(map[string]any)
	if !ok {
		*violations = append(*violations, path+" must be a schema object")
		return
	}
	keys := make([]string, 0, len(record))
	for key := range record {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if schemaConstraintKeywords[key] {
			continue
		}
		if schemaAnnotationKeywords[key] {
			if (key == "description" || key == "title") && !isJSONString(record[key]) {
				*violations = append(*violations, path+"."+key+" must be a string")
			}
			continue
		}
		*violations = append(*violations, path+"."+key+" is not a supported keyword (subset: type/oneOf/properties/required/additionalProperties/items/enum/const + annotations)")
	}
	_, hasType := record["type"]
	oneOf, hasOneOf := record["oneOf"]
	if hasType && hasOneOf {
		*violations = append(*violations, path+" cannot declare both type and oneOf")
		return
	}
	if !hasType && !hasOneOf {
		for _, key := range oneOfSiblingKeywords {
			if _, present := record[key]; present {
				*violations = append(*violations, path+"."+key+" requires type or oneOf")
			}
		}
		return
	}
	if hasOneOf {
		for _, key := range oneOfSiblingKeywords {
			if _, present := record[key]; present {
				*violations = append(*violations, path+"."+key+" is not supported beside oneOf")
			}
		}
		branches, ok := oneOf.([]any)
		if !ok || len(branches) < 2 {
			*violations = append(*violations, path+".oneOf must be an array of at least two schemas")
			return
		}
		for index, branch := range branches {
			checkSchemaNode(branch, fmt.Sprintf("%s.oneOf[%d]", path, index), violations)
		}
		return
	}
	schemaType, ok := record["type"].(string)
	if !ok {
		if _, isArray := record["type"].([]any); isArray {
			*violations = append(*violations, path+".type must be a single type string (type arrays are not supported)")
			return
		}
		*violations = append(*violations, path+".type must be one of object/array/string/number/integer/boolean/null")
		return
	}
	if !schemaTypes[schemaType] {
		*violations = append(*violations, path+".type must be one of object/array/string/number/integer/boolean/null")
		return
	}
	allowed := make([]string, 0, len(schemaKeywordTypes))
	for key := range schemaKeywordTypes {
		allowed = append(allowed, key)
	}
	sort.Strings(allowed)
	for _, key := range allowed {
		if _, present := record[key]; !present {
			continue
		}
		if !containsString(schemaKeywordTypes[key], schemaType) {
			*violations = append(*violations, fmt.Sprintf("%s.%s is not supported on type %q", path, key, schemaType))
		}
	}
	switch schemaType {
	case "object":
		checkObjectSchema(record, path, violations)
	case "array":
		if items, present := record["items"]; present {
			checkSchemaNode(items, path+".items", violations)
		}
	default:
		checkScalarSchema(record, path, schemaType, violations)
	}
}

func checkObjectSchema(record map[string]any, path string, violations *[]string) {
	properties, hasProperties := record["properties"]
	declared, declaredOK := properties.(map[string]any)
	if hasProperties {
		if !declaredOK {
			*violations = append(*violations, path+".properties must be an object of schemas")
		} else {
			names := make([]string, 0, len(declared))
			for name := range declared {
				names = append(names, name)
			}
			sort.Strings(names)
			for _, name := range names {
				checkSchemaNode(declared[name], path+".properties."+name, violations)
			}
		}
	}
	if required, present := record["required"]; present {
		names, ok := required.([]any)
		if !ok {
			*violations = append(*violations, path+".required must be an array of strings")
		} else {
			for _, entry := range names {
				name, isString := entry.(string)
				if !isString {
					*violations = append(*violations, path+".required must be an array of strings")
					break
				}
				// A name that is not declared is refused: a required property
				// the script never described is not a constraint the child
				// can be held to.
				if _, present := declared[name]; !present {
					*violations = append(*violations, fmt.Sprintf("%s.required names %q which is not in properties", path, name))
				}
			}
		}
	}
	if additional, present := record["additionalProperties"]; present {
		if _, ok := additional.(bool); !ok {
			*violations = append(*violations, path+".additionalProperties must be a boolean")
		}
	}
}

func checkScalarSchema(record map[string]any, path, schemaType string, violations *[]string) {
	enum, hasEnum := record["enum"]
	enumValid := false
	if hasEnum {
		entries, ok := enum.([]any)
		enumValid = ok && len(entries) > 0
		if enumValid {
			for _, entry := range entries {
				if !scalarMatches(schemaType, entry) {
					enumValid = false
					break
				}
			}
		}
		if !enumValid {
			*violations = append(*violations, fmt.Sprintf("%s.enum must be a non-empty array of %s values", path, schemaType))
		}
	}
	if declared, present := record["const"]; present {
		if !scalarMatches(schemaType, declared) {
			*violations = append(*violations, fmt.Sprintf("%s.const must be a %s value", path, schemaType))
		} else if enumValid && !enumContains(enum, declared) {
			*violations = append(*violations, fmt.Sprintf("%s.const must be one of %s.enum when both are declared", path, path))
		}
	}
}

func scalarMatches(schemaType string, value any) bool {
	switch schemaType {
	case "string":
		_, ok := value.(string)
		return ok
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "null":
		return value == nil
	case "number":
		return isJSONNumber(value)
	case "integer":
		number, ok := jsonNumber(value)
		return ok && number == math.Trunc(number)
	}
	return false
}

func isJSONNumber(value any) bool {
	_, ok := jsonNumber(value)
	return ok
}

func jsonNumber(value any) (float64, bool) {
	switch typed := value.(type) {
	case json.Number:
		number, err := typed.Float64()
		return number, err == nil
	case float64:
		return typed, true
	}
	return 0, false
}

func enumContains(enum any, value any) bool {
	entries, ok := enum.([]any)
	if !ok {
		return false
	}
	for _, entry := range entries {
		if jsonEqual(entry, value) {
			return true
		}
	}
	return false
}

func jsonEqual(left, right any) bool {
	leftNumber, leftIsNumber := jsonNumber(left)
	rightNumber, rightIsNumber := jsonNumber(right)
	if leftIsNumber && rightIsNumber {
		return leftNumber == rightNumber
	}
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	if leftErr != nil || rightErr != nil {
		return false
	}
	return string(leftJSON) == string(rightJSON)
}

func isJSONString(value any) bool {
	_, ok := value.(string)
	return ok
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
