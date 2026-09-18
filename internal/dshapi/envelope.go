package dshapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
)

// Error codes on the wire. gateway/* spellings mirror the upstream gateway's
// own vocabulary so a console that special-cases them keeps working; the
// session/* codes are this host's additions for the failures the console
// surfaces in a session panel.
const (
	codeBadRequest         = "gateway/bad-request"
	codeArgumentsInvalid   = "gateway/arguments-invalid"
	codeInternal           = "gateway/internal"
	codeServiceUnavailable = "gateway/service-unavailable"
	codeLimitExceeded      = "gateway/limit-exceeded"
	// codeUnimplemented is the honest answer for a method this host knows
	// about but cannot implement without inventing semantics the repository
	// does not have.
	codeUnimplemented      = "unimplemented"
	codeSessionNotFound    = "session/not-found"
	codeSessionConflict    = "session/conflict"
	codeUnsupportedContent = "session/unsupported-content"
	codeTitleInvalid       = "session/title-invalid"
)

// maxRequestBodyBytes bounds one unary request. Console envelopes and text
// prompts are small; a body this large is a bug or an allocation attack, and
// it is cheaper to refuse it before decoding than after.
const maxRequestBodyBytes = 8 << 20

// endpointSegmentPattern is the wire's accepted endpoint alphabet
// (client/rpc.ts assertTarget). Both the namespace and the method must be a
// single segment drawn from it, which is also what lets $events/result name a
// namespace starting with '$'.
var endpointSegmentPattern = regexp.MustCompile(`^[A-Za-z0-9_$.-]+$`)

// rpcResult is one arm of the server-response result union. Value is only set
// for the ok arm; Error is only set for the failed arm.
type rpcResult struct {
	OK    bool      `json:"ok"`
	Value any       `json:"value,omitempty"`
	Error *rpcError `json:"error,omitempty"`
}

// rpcError is the failure arm of a result. Details is always written, even
// when empty: the shipped client's parseConnectionResponse requires
// isRecord(error.details) and throws a TypeError on a missing field, even
// though the TypeScript type marks it optional. Omitting it would turn a
// method-level failure into an unmatched envelope.
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

// envelope is one validated client-request. rawRPCID keeps the exact bytes of
// the rpcId JSON value so the reply can echo them without a re-encoding round
// trip; the console compares the decoded value, and raw echo keeps even the
// spelling identical.
type envelope struct {
	rawRPCID json.RawMessage
	method   string
	args     map[string]json.RawMessage
}

// parseEnvelope validates the request envelope and returns a human-readable
// problem string when it is not one. Every problem here is a 400-level
// protocol error: the body cannot be trusted to identify an endpoint or a
// correlation id, so there is no result envelope to answer with.
func parseEnvelope(body []byte) (envelope, string) {
	top, err := decodeJSONObject(body)
	if err != nil {
		return envelope{}, "request body must be a JSON object: " + err.Error()
	}
	// Resolve rpcId first so a later problem can still report which call was
	// refused. The correlation id is the one field worth keeping even when the
	// rest of the envelope is unusable.
	rawRPCID, ok := top["rpcId"]
	if !ok {
		return envelope{}, `request body is missing "rpcId"`
	}
	var rpcID string
	if err := json.Unmarshal(rawRPCID, &rpcID); err != nil || rpcID == "" {
		return envelope{}, `"rpcId" must be a non-empty string`
	}
	request := envelope{rawRPCID: rawRPCID}
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
// the last duplicate, so the payload's "exactly one args" rule needs a decoder
// that sees every key the body actually contains.
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

// endpointFromPath extracts <namespace>/<method> from an /api request path.
// Anything else is not an endpoint this handler owns and must 404, which is
// exactly how a partial host degrades one feature without failing boot.
func endpointFromPath(path string) (string, bool) {
	const prefix = "/api/"
	if len(path) <= len(prefix) || path[:len(prefix)] != prefix {
		return "", false
	}
	endpoint := path[len(prefix):]
	segments := splitEndpoint(endpoint)
	if len(segments) != 2 {
		return "", false
	}
	for _, segment := range segments {
		if segment == "" || segment == "." || segment == ".." || !endpointSegmentPattern.MatchString(segment) {
			return "", false
		}
	}
	return endpoint, true
}

func splitEndpoint(endpoint string) []string {
	var segments []string
	start := 0
	for index := 0; index <= len(endpoint); index++ {
		if index == len(endpoint) || endpoint[index] == '/' {
			segments = append(segments, endpoint[start:index])
			start = index + 1
		}
	}
	return segments
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
// the request never reached a method, so there is no method-level result to
// report. The rpcId is repeated when the body carried a usable one, purely so
// a human reading the response can tell which call was refused.
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
// interceptor claims is a bare 404 "not found", which the client turns into a
// transport failure for that feature alone.
func writeNotFound(w http.ResponseWriter) {
	http.Error(w, "not found", http.StatusNotFound)
}

// writeForbidden refuses a request at the trust fence, before any endpoint is
// dispatched. The message names the failed check so a log or a curl shows why.
func writeForbidden(w http.ResponseWriter, reason string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusForbidden)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{"code": "forbidden", "message": reason},
	})
}
