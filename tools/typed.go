package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/feiyu912/zenforge/tool"
	"github.com/feiyu912/zenforge/tool/jsonschema"
)

type TypedTool struct {
	name        string
	description string
	schema      map[string]any
	handler     reflect.Value
	inputType   reflect.Type
	outputType  reflect.Type
	mode        handlerMode
	contextual  bool
}

type handlerMode int

const (
	modeOutput handlerMode = iota
	modeString
	modeErrorOnly
)

var (
	contextType     = reflect.TypeOf((*context.Context)(nil)).Elem()
	toolContextType = reflect.TypeOf(tool.Context{})
	errorType       = reflect.TypeOf((*error)(nil)).Elem()
	stringType      = reflect.TypeOf("")
)

func New(name, description string, handler any) (tool.Tool, error) {
	typed, err := newTypedTool(name, description, handler)
	if err != nil {
		return nil, err
	}
	return typed, nil
}

func Must(name, description string, handler any) tool.Tool {
	tool, err := New(name, description, handler)
	if err != nil {
		panic(err)
	}
	return tool
}

func newTypedTool(name, description string, handler any) (*TypedTool, error) {
	value := reflect.ValueOf(handler)
	if !value.IsValid() || value.Kind() != reflect.Func {
		return nil, fmt.Errorf("%w: handler must be a function", tool.ErrInvalidTool)
	}
	typ := value.Type()
	if (typ.NumIn() != 2 && typ.NumIn() != 3) || !typ.In(0).Implements(contextType) {
		return nil, fmt.Errorf("%w: handler must accept context.Context, input, and optional tool.Context", tool.ErrInvalidTool)
	}
	inputType := typ.In(1)
	contextual := typ.NumIn() == 3
	if contextual && typ.In(2) != toolContextType {
		return nil, fmt.Errorf("%w: third handler argument must be tool.Context", tool.ErrInvalidTool)
	}

	var mode handlerMode
	var outputType reflect.Type
	switch {
	case typ.NumOut() == 2 && typ.Out(1).Implements(errorType):
		outputType = typ.Out(0)
		if outputType == stringType {
			mode = modeString
		} else {
			mode = modeOutput
		}
	case typ.NumOut() == 1 && typ.Out(0).Implements(errorType):
		mode = modeErrorOnly
	default:
		return nil, fmt.Errorf("%w: unsupported handler signature", tool.ErrInvalidTool)
	}

	return &TypedTool{
		name:        name,
		description: description,
		schema:      jsonschema.InferType(inputType),
		handler:     value,
		inputType:   inputType,
		outputType:  outputType,
		mode:        mode,
		contextual:  contextual,
	}, nil
}

func (t *TypedTool) Name() string {
	return t.name
}

func (t *TypedTool) Description() string {
	return t.description
}

func (t *TypedTool) Schema() map[string]any {
	return t.schema
}

func (t *TypedTool) Call(ctx context.Context, input json.RawMessage, call tool.Context) (tool.Result, error) {
	in, err := decodeInput(input, t.inputType)
	if err != nil {
		return tool.Result{Error: tool.ErrInvalidArguments.Error(), ExitCode: 1}, fmt.Errorf("%w: %v", tool.ErrInvalidArguments, err)
	}
	args := []reflect.Value{reflect.ValueOf(ctx), in}
	if t.contextual {
		args = append(args, reflect.ValueOf(call))
	}
	out := t.handler.Call(args)
	switch t.mode {
	case modeErrorOnly:
		if err := valueError(out[0]); err != nil {
			return tool.Result{Error: err.Error(), ExitCode: 1}, err
		}
		return tool.Result{}, nil
	case modeString:
		if err := valueError(out[1]); err != nil {
			return tool.Result{Error: err.Error(), ExitCode: 1}, err
		}
		return tool.Result{Output: out[0].String()}, nil
	default:
		if err := valueError(out[1]); err != nil {
			return tool.Result{Error: err.Error(), ExitCode: 1}, err
		}
		return encodeOutput(out[0].Interface())
	}
}

func decodeInput(raw json.RawMessage, typ reflect.Type) (reflect.Value, error) {
	targetType := typ
	pointer := false
	if typ.Kind() == reflect.Pointer {
		pointer = true
		targetType = typ.Elem()
	}
	target := reflect.New(targetType)
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
		decoder = json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
	}
	if err := decoder.Decode(target.Interface()); err != nil {
		return reflect.Value{}, err
	}
	if pointer {
		return target, nil
	}
	return target.Elem(), nil
}

func encodeOutput(output any) (tool.Result, error) {
	if output == nil {
		return tool.Result{}, nil
	}
	if text, ok := output.(string); ok {
		return tool.Result{Output: text}, nil
	}
	data, err := json.Marshal(output)
	if err != nil {
		return tool.Result{}, err
	}
	var structured map[string]any
	if err := json.Unmarshal(data, &structured); err != nil {
		return tool.Result{Output: string(data)}, nil
	}
	return tool.Result{Output: string(data), Structured: structured}, nil
}

func valueError(value reflect.Value) error {
	if value.IsNil() {
		return nil
	}
	return value.Interface().(error)
}
