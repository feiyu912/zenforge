package dshstream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/feiyu912/zenforge/approval"
)

// serveResult answers POST /api/$events/result: the out-of-band answer to one
// forwarded approval waterfall. It is the route the console's approval panel
// calls after the user presses Allow once or Reject
// (packages/api/gateway/src/client/remote-events.ts connection.rpc.call).
func (h *Handler) serveResult(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeProtocolError(w, http.StatusMethodNotAllowed, nil, codeBadRequest, "the event result endpoint requires POST")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBodyBytes+1))
	if err != nil {
		writeProtocolError(w, http.StatusBadRequest, nil, codeBadRequest, "request body could not be read")
		return
	}
	if len(body) > maxRequestBodyBytes {
		writeProtocolError(w, http.StatusBadRequest, nil, codeBadRequest, "request body exceeds the size limit")
		return
	}
	request, problem := parseResultEnvelope(body)
	if problem != "" {
		writeProtocolError(w, http.StatusBadRequest, request.rawRPCID, codeBadRequest, problem)
		return
	}
	// The body and the URL must name the same endpoint. Trusting either alone
	// would let a request logged against one method mutate another.
	const resultMethod = "$events/result"
	if request.method != resultMethod {
		writeProtocolError(w, http.StatusBadRequest, request.rawRPCID, codeBadRequest,
			fmt.Sprintf("method %q does not match endpoint %q", request.method, resultMethod))
		return
	}
	failure := h.answerApproval(r.Context(), request.args)
	if failure != nil {
		writeResult(w, request.rawRPCID, rpcResult{Error: &rpcError{
			Code: failure.code, Message: failure.message, Details: failure.details,
		}})
		return
	}
	writeResult(w, request.rawRPCID, rpcResult{OK: true})
}

// answerApproval maps one validated result onto a decision for the pending
// approval broker.
//
// Fail-closed is the whole design of this function. The only accepted answers
// are allowed-once and rejected. A delegated answer ("next"), a client-side
// listener rejection, an unsupported outcome value, an unknown clientId, an
// unknown eventId, an expired request, and a request somebody already answered
// are all errors — none of them is allowed to become an implicit allow. The
// console's own vocabulary is wider than this host can honour
// (allowed-once | rejected | cancelled | unavailable in
// interaction/user-approval/src/types.ts), and the unrepresentable half is
// refused rather than guessed.
func (h *Handler) answerApproval(ctx context.Context, args map[string]json.RawMessage) *methodError {
	clientID, _, clientFailure := stringArg(args, "clientId")
	if clientFailure != nil {
		return streamFailureToMethod(clientFailure)
	}
	eventID, _, eventFailure := stringArg(args, "eventId")
	if eventFailure != nil {
		return streamFailureToMethod(eventFailure)
	}
	rawOutcome, ok := args["outcome"]
	if !ok {
		return argumentRequired("outcome")
	}
	if clientID == "" {
		return argumentRequired("clientId")
	}
	if eventID == "" {
		return argumentRequired("eventId")
	}
	if !h.clientActive(clientID) {
		return fail(codeArgumentsInvalid,
			fmt.Sprintf("clientId %q identifies no active $events stream; reconnect and answer the approval it delivered", clientID),
			map[string]any{"clientId": clientID})
	}

	decision, failure := decodeApprovalOutcome(eventID, rawOutcome)
	if failure != nil {
		return failure
	}

	pending, err := h.inbox.Lookup(ctx, eventID)
	switch {
	case errors.Is(err, approval.ErrRequestNotFound):
		return fail(codeApprovalNotFound,
			fmt.Sprintf("approval %q is not pending: it was already answered, expired, or never existed", eventID),
			map[string]any{"eventId": eventID})
	case err != nil:
		return fail(codeInternal, "look up approval: "+err.Error(), nil)
	}
	if pending.ExpiresAt != nil && !pending.ExpiresAt.After(time.Now()) {
		// The broker may still hold an expired request; answering it must not
		// revive it.
		return fail(codeApprovalConflict,
			fmt.Sprintf("approval %q has expired", eventID),
			map[string]any{"eventId": eventID})
	}
	if err := h.inbox.Submit(ctx, decision); err != nil {
		switch {
		case errors.Is(err, approval.ErrRequestNotFound):
			return fail(codeApprovalNotFound,
				fmt.Sprintf("approval %q is no longer pending", eventID),
				map[string]any{"eventId": eventID})
		case errors.Is(err, approval.ErrDecisionConflict), errors.Is(err, approval.ErrRequestExpired):
			return fail(codeApprovalConflict, "approval could not be answered: "+err.Error(),
				map[string]any{"eventId": eventID})
		default:
			return fail(codeInternal, "submit approval decision: "+err.Error(), nil)
		}
	}
	return nil
}

// decodeApprovalOutcome validates the client's outcome union and maps the two
// answerable values onto broker decisions. The result route's outcome shape is
// parseRemoteEventResult's:
//
//	{kind:"next"}
//	{kind:"result"} | {kind:"result", value}
//	{kind:"rejected", error}
func decodeApprovalOutcome(eventID string, raw json.RawMessage) (approval.Decision, *methodError) {
	outcome, err := decodeJSONObject(raw)
	if err != nil {
		return approval.Decision{}, fail(codeArgumentsInvalid, `"outcome" must be a JSON object`,
			map[string]any{"argument": "outcome"})
	}
	kind := ""
	if rawKind, ok := outcome["kind"]; ok {
		if err := json.Unmarshal(rawKind, &kind); err != nil {
			return approval.Decision{}, fail(codeArgumentsInvalid, `"outcome.kind" must be a string`,
				map[string]any{"argument": "outcome"})
		}
	}
	switch kind {
	case "result":
		if len(outcome) > 2 {
			return approval.Decision{}, fail(codeArgumentsInvalid,
				`outcome "result" accepts only "kind" and "value"`,
				map[string]any{"argument": "outcome"})
		}
		rawValue, ok := outcome["value"]
		if !ok {
			return approval.Decision{}, argumentRequired("outcome.value")
		}
		var value string
		if err := json.Unmarshal(rawValue, &value); err != nil {
			return approval.Decision{}, fail(codeArgumentsInvalid, `"outcome.value" must be a string`,
				map[string]any{"argument": "outcome"})
		}
		var action approval.DecisionAction
		switch value {
		case "allowed-once":
			action = approval.DecisionApprove
		case "rejected":
			action = approval.DecisionReject
		default:
			return approval.Decision{}, fail(codeArgumentsInvalid,
				fmt.Sprintf("approval outcome %q is not supported: this host accepts %q or %q",
					value, "allowed-once", "rejected"),
				map[string]any{"argument": "outcome", "value": value})
		}
		return approval.Decision{
			RequestID: eventID,
			Action:    action,
			Scope:     approval.ScopeOnce,
			DecidedAt: time.Now().UTC(),
		}, nil
	case "next":
		return approval.Decision{}, fail(codeUnimplemented,
			`approval outcome "next" delegates the request and never allows it; this host has no further answerer to delegate to`,
			map[string]any{"argument": "outcome"})
	case "rejected":
		return approval.Decision{}, fail(codeUnimplemented,
			`approval outcome "rejected" reports a client-side listener failure, not a decision; answer with {"kind":"result","value":"rejected"} to deny the tool call`,
			map[string]any{"argument": "outcome"})
	default:
		return approval.Decision{}, fail(codeArgumentsInvalid,
			fmt.Sprintf("approval outcome kind %q is unknown", kind),
			map[string]any{"argument": "outcome"})
	}
}

// streamFailureToMethod converts an argument-level stream error into the
// equivalent method-level failure for the unary result envelope.
func streamFailureToMethod(failure *streamError) *methodError {
	return fail(failure.code, failure.message, failure.details)
}
