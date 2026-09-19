package dshapi

import "encoding/json"

// unwrapRequestArguments flattens the named request parameter the shipped
// console sends.
//
// The client's generated remote map declares its named parameters literally: 21
// methods take one called "request" (session/create, session/prompt,
// session/page, session/cancel, session/rename, session/selectModel, and every
// member of the workspace namespace) and session/list takes one called
// "_request" (api/session-controller/lib/typert.remote-client.d.ts:38-52,
// api/workspace-controller/lib/typert.remote-client.d.ts:29-35). The gateway
// passes named arguments through, so those calls arrive as
//
//	{"request": {"address": {...}, "maxMessages": 50}}
//	{"request": {"provider": "qwen"}, "settingsNs": "llm-pi-ai"}
//	{"_request": {}}
//
// and a handler that reads the fields directly would see none of them. That is
// exactly how session/create used to ignore its workspaceId: the console kept
// adopting workspaces and the host kept creating sessions with no grouping,
// which left the session list, the model picker and the transcript all empty
// while every individual call looked answered.
//
// A sibling named parameter stays where it is, because only the request object's
// own fields move up. Anything that is not a JSON object under those keys is
// left untouched, so a malformed request still reaches the handler's own
// validation and is refused by the code that knows the field.
func unwrapRequestArguments(args map[string]json.RawMessage) map[string]json.RawMessage {
	for _, key := range []string{"request", "_request"} {
		raw, ok := args[key]
		if !ok {
			continue
		}
		var inner map[string]json.RawMessage
		if err := json.Unmarshal(raw, &inner); err != nil || inner == nil {
			continue
		}
		flat := make(map[string]json.RawMessage, len(inner)+len(args))
		for name, value := range inner {
			flat[name] = value
		}
		for name, value := range args {
			if name == key {
				continue
			}
			if _, taken := flat[name]; !taken {
				flat[name] = value
			}
		}
		return flat
	}
	return args
}
