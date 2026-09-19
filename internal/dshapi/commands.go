package dshapi

import (
	"context"
	"encoding/json"
	"strconv"
	"time"
)

// Wire shapes for POST /api/commands/{list,execute}, the namespace the
// composer's slash menu reads: list fills the menu (ui-commands/src/client/
// service.ts:90) and execute admits a submitted line (service.ts:387, also
// ui-plan/src/client/index.ts:106). Types mirror
// packages/interaction/commands/lib/types/types.d.ts:9-29.
//
// execute is admission semantics, not output: upstream reports success once the
// host durably logged the command's lifecycle, and reports "unknown" by leaving
// the value out entirely -- the composer turns a missing value into "unknown or
// malformed command" (service.ts:390) and keeps the draft for correction when the
// outcome is an error (service.ts:393-396). Returning a value the client does not
// expect would be worse than returning none.

// CommandInputDescriptor describes the arguments a command takes. Upstream
// types.d.ts:9-12.
type CommandInputDescriptor struct {
	Hint string `json:"hint"`
	// Attachments is false for every command here: this host's commands are text
	// templates, so a submission never carries an attachment.
	Attachments bool `json:"attachments,omitempty"`
}

// CommandDescriptor is one entry in the composer's slash menu. Upstream
// types.d.ts:23-29.
type CommandDescriptor struct {
	DefinitionID string                  `json:"definitionId,omitempty"`
	Name         string                  `json:"name"`
	Description  string                  `json:"description"`
	Input        *CommandInputDescriptor `json:"input,omitempty"`
}

// CommandResult is a command's outcome. Upstream types.d.ts:14-21. This host
// reports success without text: the command's effect is the run it starts, and
// the operator sees that run in the conversation rather than as an echoed
// sentence.
type CommandResult struct {
	Kind           string `json:"kind"`
	Text           string `json:"text,omitempty"`
	SourceEventSeq *int64 `json:"sourceEventSeq,omitempty"`
}

// CommandExecution is execute's value. Upstream types.d.ts:18-21.
type CommandExecution struct {
	CommandID string        `json:"commandId"`
	Result    CommandResult `json:"result"`
}

// CommandSource is the injected command catalog: what the menu lists and what a
// submitted line expands to.
type CommandSource interface {
	// Commands lists the resolvable commands, in listing order.
	Commands() []CommandDescriptor
	// Expand resolves one submitted line into the task text it stands for,
	// reporting false when the line names no command this host has.
	Expand(line string) (name, text string, ok bool)
}

// SetCommands installs the catalog the commands methods answer from.
func (h *Handler) SetCommands(source CommandSource) {
	h.commandsMu.Lock()
	h.commands = source
	h.commandsMu.Unlock()
}

func (h *Handler) commandSource() CommandSource {
	h.commandsMu.RLock()
	defer h.commandsMu.RUnlock()
	return h.commands
}

// commandsList answers POST /api/commands/list. The answer is always an array: an
// empty menu is a fact, and null would read as a failure.
func (h *Handler) commandsList(ctx context.Context, args map[string]json.RawMessage) (any, *methodError) {
	if _, failure := h.commandsScope(ctx, args); failure != nil {
		return nil, failure
	}
	source := h.commandSource()
	if source == nil {
		return nil, commandsDependencyMissing("commands/list")
	}
	commands := source.Commands()
	if commands == nil {
		commands = []CommandDescriptor{}
	}
	return commands, nil
}

// commandsExecute answers POST /api/commands/execute: it resolves the line,
// starts the run it stands for, and reports admission. A line this host has no
// command for is answered without a value, which is how the composer knows to
// keep the draft and say the command is unknown.
func (h *Handler) commandsExecute(ctx context.Context, args map[string]json.RawMessage) (any, *methodError) {
	agentID, failure := h.commandsScope(ctx, args)
	if failure != nil {
		return nil, failure
	}
	line, present, failure := stringArg(args, "line")
	if failure != nil {
		return nil, failure
	}
	if !present {
		return nil, argumentRequired("line")
	}
	if raw, ok := args["submittedAttachments"]; ok {
		var attachments []json.RawMessage
		if err := json.Unmarshal(raw, &attachments); err != nil {
			return nil, fail(codeBadRequest, `argument "submittedAttachments" must be an array`,
				map[string]any{"argument": "submittedAttachments"})
		}
		if len(attachments) > 0 {
			// No command this host lists advertises attachments, so a submission
			// carrying one is refused by name rather than dropped: the operator
			// would otherwise watch an attachment silently disappear.
			return nil, fail(codeUnimplemented,
				"this host's commands take no attachments; remove the attachment and submit the command again",
				map[string]any{"capability": "command attachments"})
		}
	}
	source := h.commandSource()
	if source == nil {
		return nil, commandsDependencyMissing("commands/execute")
	}
	name, text, ok := source.Expand(line)
	if !ok {
		// No value: the composer reports "unknown or malformed command" and keeps
		// what the operator typed.
		return nil, nil
	}
	// The command's effect is the run it stands for. Submitting the expanded text
	// is the same path a typed prompt takes, so the command's output appears in
	// the conversation exactly as a normal turn would -- and the expansion is the
	// host's own, so a command's includes, arguments and shell permission follow
	// the rules this host already enforces.
	promptArgs := map[string]json.RawMessage{
		"requestId": jsonArgument("command-" + strconv.FormatInt(time.Now().UnixNano(), 10)),
		"sessionId": jsonArgument(agentID),
		"mode":      jsonArgument("queue"),
		"content":   jsonArgument([]map[string]any{{"type": "text", "text": text}}),
	}
	if _, failure := h.sessionPrompt(ctx, promptArgs); failure != nil {
		return nil, failure
	}
	return CommandExecution{CommandID: name, Result: CommandResult{Kind: "success"}}, nil
}

// commandsScope reads and validates the session a command call names. Commands are
// per session because the console asks on behalf of one conversation, and an
// unknown session is refused here so the failure names the session rather than
// arriving later as a confusing run error.
func (h *Handler) commandsScope(ctx context.Context, args map[string]json.RawMessage) (string, *methodError) {
	agentID, _, failure := stringArg(args, "agentId")
	if failure != nil {
		return "", failure
	}
	if agentID == "" {
		return "", argumentRequired("agentId")
	}
	if !h.sessionKnown(ctx, agentID) {
		return "", fail(codeSessionNotFound,
			"command target "+strconv.Quote(agentID)+" is not a session this host knows",
			map[string]any{"sessionId": agentID})
	}
	return agentID, nil
}

// jsonArgument encodes one synthesized argument for the prompt path.
func jsonArgument(value any) json.RawMessage {
	encoded, err := json.Marshal(value)
	if err != nil {
		// The values here are strings and a text content part, so this cannot
		// fail; returning null keeps the shape valid if it somehow did.
		return json.RawMessage("null")
	}
	return encoded
}

// commandsDependencyMissing is the honest answer when no catalog is installed.
func commandsDependencyMissing(method string) *methodError {
	return fail(codeUnimplemented,
		method+" is not configured: the host has no command catalog; the serve command must install one with Handler.SetCommands",
		map[string]any{"dependency": "CommandSource"})
}
