package dshstream

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
)

// rpcResult, rpcError, wireResult and the envelope parser below are duplicated
// from internal/dshapi. That package owns the unary half of the console
// protocol and keeps its parser, its result envelope, and its error codes
// unexported; this package must not edit it. The duplication is deliberately
// small and shape-identical (including the raw-rpcId echo and the always
// present details object) so the two halves cannot drift on the properties the
// shipped client enforces: rpcId echoed exactly, result.ok true/false, and a
// details record on every failure.

// rpcResult is one arm of the server-response result union. Value is only set
// for the ok arm; Error is only set for the failed arm.
type rpcResult struct {
	OK    bool      `json:"ok"`
	Value any       `json:"value,omitempty"`
	Error *rpcError `json:"error,omitempty"`
}

// rpcError is the failure arm of a result. Details is always written, even
// when empty: the shipped client requires isRecord(error.details).
type rpcError struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details"`
}

// methodError is a method-level failure: the envelope was well formed and the
// endpoint is known, so the reply is a normal result.ok:false value at HTTP
// 200. Protocol failures are handled separately and never become one of these.
type methodError struct {
	code    string
	message string
	details map[string]any
}

func fail(code, message string, details map[string]any) *methodError {
	if details == nil {
		details = map[string]any{}
	}
	return &methodError{code: code, message: message, details: details}
}

func argumentRequired(name string) *methodError {
	return fail(codeArgumentsInvalid, fmt.Sprintf("argument %q is required", name), map[string]any{"argument": name})
}

// resultEnvelope is one validated POST /api/$events/result request. rawRPCID
// keeps the exact bytes of the rpcId JSON value so the reply can echo them
// without a re-encoding round trip.
type resultEnvelope struct {
	rawRPCID json.RawMessage
	method   string
	args     map[string]json.RawMessage
}

// parseResultEnvelope validates the request envelope and returns a
// human-readable problem string when it is not one. Every problem here is a
// 400-level protocol error: the body cannot be trusted to identify an endpoint
// or a correlation id, so there is no result envelope to answer with.
func parseResultEnvelope(body []byte) (resultEnvelope, string) {
	top, err := decodeJSONObject(body)
	if err != nil {
		return resultEnvelope{}, "request body must be a JSON object: " + err.Error()
	}
	rawRPCID, ok := top["rpcId"]
	if !ok {
		return resultEnvelope{}, `request body is missing "rpcId"`
	}
	var rpcID string
	if err := json.Unmarshal(rawRPCID, &rpcID); err != nil || rpcID == "" {
		return resultEnvelope{}, `"rpcId" must be a non-empty string`
	}
	request := resultEnvelope{rawRPCID: rawRPCID}
	rawType, ok := top["type"]
	if !ok {
		return request, `request body is missing "type"`
	}
	var messageType string
	if err := json.Unmarshal(rawType, &messageType); err != nil || messageType != "client-request" {
		return request, `"type" must be "client-request"`
	}
	rawMethod, ok := top["method"]
	if !ok {
		return request, `request body is missing "method"`
	}
	var method string
	if err := json.Unmarshal(rawMethod, &method); err != nil {
		return request, `"method" must be a string`
	}
	rawPayload, ok := top["payload"]
	if !ok {
		return request, `request body is missing "payload"`
	}
	payload, err := decodeJSONObject(rawPayload)
	if err != nil {
		return request, `"payload" must be a JSON object: ` + err.Error()
	}
	if len(payload) != 1 {
		return request, `"payload" must contain exactly one field, "args"`
	}
	rawArgs, ok := payload["args"]
	if !ok {
		return request, `"payload" must contain exactly one field, "args"`
	}
	args, err := decodeJSONObject(rawArgs)
	if err != nil {
		return request, `payload "args" must be a plain JSON object: ` + err.Error()
	}
	request.method = method
	request.args = args
	return request, ""
}

// decodeJSONObject reads one JSON object, rejecting duplicate keys and any
// non-object top level. A plain map[string]json.RawMessage would silently keep
// the last duplicate, so the exact-key rules need a decoder that sees every
// key the body actually contains.
func decodeJSONObject(raw []byte) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil {
		return nil, errors.New("not valid JSON")
	}
	delim, ok := token.(json.Delim)
	if !ok || delim != '{' {
		return nil, errors.New("not a JSON object")
	}
	object := make(map[string]json.RawMessage)
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return nil, errors.New("not valid JSON")
		}
		key, ok := keyToken.(string)
		if !ok {
			return nil, errors.New("not a JSON object")
		}
		if _, duplicate := object[key]; duplicate {
			return nil, fmt.Errorf("duplicate field %q", key)
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, errors.New("not valid JSON")
		}
		object[key] = value
	}
	if _, err := decoder.Token(); err != nil {
		return nil, errors.New("not valid JSON")
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, errors.New("unexpected trailing data")
	}
	return object, nil
}

// writeResult writes a server-response envelope. The rpcId is spliced in as
// raw bytes so the reply echoes exactly what the client sent; a reply the
// client cannot match is indistinguishable from a hang on its side.
func writeResult(w http.ResponseWriter, rawRPCID json.RawMessage, result rpcResult) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	var buffer bytes.Buffer
	buffer.WriteString(`{"type":"server-response","rpcId":`)
	buffer.Write(rawRPCID)
	buffer.WriteString(`,"result":`)
	encoded, err := json.Marshal(result)
	if err != nil {
		// Only reachable if a handler returns a value that cannot be encoded;
		// fail closed with a well-formed envelope rather than an empty body.
		encoded = []byte(`{"ok":false,"error":{"code":"gateway/internal","message":"response encoding failed","details":{}}}`)
	}
	buffer.Write(encoded)
	buffer.WriteByte('}')
	_, _ = w.Write(buffer.Bytes())
}

// writeProtocolError answers a malformed request. It is not a result envelope:
// the request never reached the endpoint, so there is no method-level result
// to report. The rpcId is repeated when the body carried a usable one, purely
// so a human reading the response can tell which call was refused.
func writeProtocolError(w http.ResponseWriter, status int, rawRPCID json.RawMessage, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	body := map[string]any{"error": map[string]any{"code": code, "message": message}}
	if len(rawRPCID) > 0 {
		body["rpcId"] = json.RawMessage(rawRPCID)
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		encoded = []byte(`{"error":{"code":"gateway/internal","message":"response encoding failed"}}`)
	}
	_, _ = w.Write(encoded)
}

// writeNotFound matches the upstream /api dispatcher: an endpoint no
// interceptor claims is a bare 404 "not found".
func writeNotFound(w http.ResponseWriter) {
	http.Error(w, "not found", http.StatusNotFound)
}

// writeForbidden refuses a request at the trust fence, before any endpoint or
// upgrade is dispatched. The message names the failed check so a log shows why.
func writeForbidden(w http.ResponseWriter, reason string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusForbidden)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{"code": "forbidden", "message": reason},
	})
}

// endpointArgs decodes one mux open frame's payload into its args object.
// Upstream encodes every stream request as {args:{...}} and validates the
// exact-key envelope before the endpoint runs, so a payload that is not exactly
// one "args" object is an arguments-invalid stream error, not a connection
// close.
func endpointArgs(payload json.RawMessage) (map[string]json.RawMessage, *streamError) {
	object, err := decodeJSONObject(payload)
	if err != nil {
		return nil, streamFail(codeArgumentsInvalid, "stream payload must be a JSON object containing exactly one args field",
			map[string]any{"payload": "invalid"})
	}
	if len(object) != 1 {
		return nil, streamFail(codeArgumentsInvalid, "stream payload must contain exactly one field, args",
			map[string]any{"payload": "invalid"})
	}
	rawArgs, ok := object["args"]
	if !ok {
		return nil, streamFail(codeArgumentsInvalid, "stream payload must contain exactly one field, args",
			map[string]any{"payload": "invalid"})
	}
	args, err := decodeJSONObject(rawArgs)
	if err != nil {
		return nil, streamFail(codeArgumentsInvalid, `stream payload "args" must be a plain JSON object`,
			map[string]any{"argument": "args"})
	}
	return args, nil
}

// emptyArgs reports whether an args object carries no fields. $events and
// session/control both require exactly that.
func emptyArgs(args map[string]json.RawMessage) bool { return len(args) == 0 }

// stringArg reads an optional string argument. A present non-string value is an
// argument error, not a protocol error: the envelope was fine.
func stringArg(args map[string]json.RawMessage, key string) (string, bool, *streamError) {
	raw, ok := args[key]
	if !ok {
		return "", false, nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", false, streamFail(codeArgumentsInvalid, fmt.Sprintf("argument %q must be a string", key),
			map[string]any{"argument": key})
	}
	return value, true, nil
}

// boolArg reads an optional boolean argument.
func boolArg(args map[string]json.RawMessage, key string) (bool, bool, *streamError) {
	raw, ok := args[key]
	if !ok {
		return false, false, nil
	}
	var value bool
	if err := json.Unmarshal(raw, &value); err != nil {
		return false, false, streamFail(codeArgumentsInvalid, fmt.Sprintf("argument %q must be a boolean", key),
			map[string]any{"argument": key})
	}
	return value, true, nil
}

// intArg reads an optional integer argument. JSON numbers only: a string or a
// fractional value is an argument error.
func intArg(args map[string]json.RawMessage, key string) (int64, bool, *streamError) {
	raw, ok := args[key]
	if !ok {
		return 0, false, nil
	}
	var value int64
	if err := json.Unmarshal(raw, &value); err != nil {
		return 0, false, streamFail(codeArgumentsInvalid, fmt.Sprintf("argument %q must be an integer", key),
			map[string]any{"argument": key})
	}
	return value, true, nil
}

// nilInterface catches a typed-nil dependency, which would pass a plain nil
// check and panic on first use. Duplicated from internal/dshapi for the same
// reason as the envelope parser.
func nilInterface(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}
