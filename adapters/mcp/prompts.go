package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// PromptMessage roles. They are the two the spec defines; a prompt is rendered
// as a conversation, so the role is not optional.
const (
	PromptRoleUser      = "user"
	PromptRoleAssistant = "assistant"
)

// PromptArgument declares one argument a prompt accepts. Required is what the
// server checks before calling the handler: a client that omits it gets an
// invalid-params error instead of a rendered prompt with a hole in it.
type PromptArgument struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required,omitempty"`
}

// PromptMessage is one rendered message. Content reuses the tool-call Content
// shape, so a prompt message and a tool result carry the same wire form.
type PromptMessage struct {
	Role    string  `json:"role"`
	Content Content `json:"content"`
}

// PromptResult is what a prompt handler renders. Description is an optional
// per-render note; Messages is the conversation the client will show or send.
type PromptResult struct {
	Description string          `json:"description,omitempty"`
	Messages    []PromptMessage `json:"messages"`
}

// PromptHandler renders one prompt. Arguments is keyed by the names declared in
// the prompt's Arguments. A returned error is a JSON-RPC error, not a tool
// result: prompts are not tools, and a client must not mistake an unrenderable
// prompt for a message the model should see.
type PromptHandler func(ctx context.Context, arguments map[string]string) (PromptResult, error)

// ServerPrompt is one prompt the server exposes.
type ServerPrompt struct {
	Name        string
	Description string
	Arguments   []PromptArgument
	Handler     PromptHandler
}

// listPrompts renders the registered set. Arguments are included only when a
// prompt declares some, so a client does not have to tell an empty argument
// list from an absent one.
func (s *Server) listPrompts() (json.RawMessage, *rpcError) {
	prompts := make([]map[string]any, 0, len(s.prompts))
	for _, serverPrompt := range s.prompts {
		entry := map[string]any{"name": serverPrompt.Name}
		if serverPrompt.Description != "" {
			entry["description"] = serverPrompt.Description
		}
		if len(serverPrompt.Arguments) > 0 {
			arguments := make([]map[string]any, 0, len(serverPrompt.Arguments))
			for _, argument := range serverPrompt.Arguments {
				item := map[string]any{
					"name":     argument.Name,
					"required": argument.Required,
				}
				if argument.Description != "" {
					item["description"] = argument.Description
				}
				arguments = append(arguments, item)
			}
			entry["arguments"] = arguments
		}
		prompts = append(prompts, entry)
	}
	return mustMarshal(map[string]any{"prompts": prompts}), nil
}

// getPrompt serves prompts/get. An unknown prompt name and a missing required
// argument are both invalid params (-32602, stated at the constant): from the
// client's side nothing is wrong with the server, its request needs fixing,
// and the spec has no more specific code for "you did not name a prompt that
// exists".
func (s *Server) getPrompt(ctx context.Context, params json.RawMessage) (json.RawMessage, *rpcError) {
	var request struct {
		Name      string            `json:"name"`
		Arguments map[string]string `json:"arguments"`
	}
	if err := decodeMessage(params, &request); err != nil {
		return nil, &rpcError{Code: codeInvalidParams, Message: "invalid prompts/get params: " + err.Error()}
	}
	name := strings.TrimSpace(request.Name)
	if name == "" {
		return nil, &rpcError{Code: codeInvalidParams, Message: "prompts/get needs a prompt name"}
	}
	serverPrompt, ok := s.promptByName[name]
	if !ok {
		return nil, &rpcError{Code: codeInvalidParams, Message: fmt.Sprintf("unknown prompt %q", name)}
	}
	for _, argument := range serverPrompt.Arguments {
		if !argument.Required {
			continue
		}
		// An argument present but empty is treated as missing: a template
		// cannot tell the two apart, and the handler would receive "" either
		// way, which is the hole the required check exists to prevent.
		if strings.TrimSpace(request.Arguments[argument.Name]) == "" {
			return nil, &rpcError{
				Code:    codeInvalidParams,
				Message: fmt.Sprintf("prompt %q needs the %q argument", name, argument.Name),
			}
		}
	}
	result, err := s.invokePrompt(ctx, serverPrompt, request.Arguments)
	if err != nil {
		return nil, &rpcError{Code: codeInternalError, Message: fmt.Sprintf("rendering prompt %q failed: %v", name, err)}
	}
	if result.Messages == nil {
		// The spec makes messages an array; a prompt that renders nothing
		// should answer with an empty one, not null.
		result.Messages = []PromptMessage{}
	}
	return mustMarshal(result), nil
}

// invokePrompt runs a handler, converting a panic into an error. It mirrors
// invoke for tools: a panicking prompt must fail its call and leave the stream
// alive, because the peer would otherwise see a closed pipe instead of an
// answer and the reason would be lost.
func (s *Server) invokePrompt(ctx context.Context, serverPrompt ServerPrompt, arguments map[string]string) (result PromptResult, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("mcp prompt %q panicked: %v", serverPrompt.Name, recovered)
		}
	}()
	return serverPrompt.Handler(ctx, arguments)
}
