package jsonschema

import (
	"reflect"
	"strings"
)

func Infer(value any) map[string]any {
	if value == nil {
		return map[string]any{"type": "object"}
	}
	typ := reflect.TypeOf(value)
	if typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	return schemaFor(typ)
}

func InferType(typ reflect.Type) map[string]any {
	if typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	return schemaFor(typ)
}

func schemaFor(typ reflect.Type) map[string]any {
	switch typ.Kind() {
	case reflect.Struct:
		return structSchema(typ)
	case reflect.String:
		return map[string]any{"type": "string"}
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return map[string]any{"type": "integer"}
	case reflect.Float32, reflect.Float64:
		return map[string]any{"type": "number"}
	case reflect.Slice, reflect.Array:
		return map[string]any{"type": "array", "items": schemaFor(typ.Elem())}
	case reflect.Map:
		return map[string]any{"type": "object"}
	case reflect.Pointer:
		return schemaFor(typ.Elem())
	default:
		return map[string]any{"type": "object"}
	}
}

func structSchema(typ reflect.Type) map[string]any {
	properties := map[string]any{}
	var required []string
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		if field.PkgPath != "" {
			continue
		}
		name, omitEmpty, skip := fieldName(field)
		if skip {
			continue
		}
		property := schemaFor(field.Type)
		if description := schemaDescription(field.Tag.Get("jsonschema")); description != "" {
			property["description"] = description
		}
		properties[name] = property
		if schemaRequired(field.Tag.Get("jsonschema")) || !omitEmpty {
			required = append(required, name)
		}
	}
	schema := map[string]any{
		"type":                 "object",
		"properties":           properties,
		"additionalProperties": false,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func fieldName(field reflect.StructField) (name string, omitEmpty bool, skip bool) {
	name = field.Name
	tag := field.Tag.Get("json")
	if tag == "-" {
		return "", false, true
	}
	if tag != "" {
		parts := strings.Split(tag, ",")
		if parts[0] != "" {
			name = parts[0]
		}
		for _, part := range parts[1:] {
			if part == "omitempty" {
				omitEmpty = true
				break
			}
		}
	}
	return name, omitEmpty, false
}

func schemaRequired(tag string) bool {
	for _, part := range strings.Split(tag, ",") {
		if strings.TrimSpace(part) == "required" {
			return true
		}
	}
	return false
}

func schemaDescription(tag string) string {
	for _, part := range strings.Split(tag, ",") {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(part, "description=") {
			return strings.TrimPrefix(part, "description=")
		}
	}
	return ""
}
