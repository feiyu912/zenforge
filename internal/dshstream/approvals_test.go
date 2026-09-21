package dshstream

import (
	"net/http"
	"testing"
	"time"

	"github.com/feiyu912/zenforge/approval"
	"github.com/feiyu912/zenforge/internal/dshsession"
)

// A conversation's later turn runs under "<session>~<turn>" (ADR 0108), but the
// console only shows an approval for a session it has open: its panel resolves
// the event's agent through ctx.sessions.scopeOf and delegates onward ("next()")
// when that fails. A waterfall that named the turn was therefore delivered and
// dropped, and the turn waited for an answer nobody could give.
func TestApprovalWaterfallNamesTheSessionNotTheTurn(t *testing.T) {
	f := newFixture(t, Config{})
	sessionID := f.startRun(t, "scoped")
	turnRunID := dshsession.ContinuationRunID(sessionID, 2)
	request := newApprovalRequest("approval-scoped", turnRunID)
	pendingDecision(t, f.broker, request)
	<-f.broker.Requests()

	conn := f.mustDial(t)
	openEventsStream(t, conn, "events")
	waterfall := readItem(t, conn, "events")
	assertField(t, waterfall, "type", "waterfall")

	agentID, _ := stringField(t, waterfall, "agentId")
	if agentID != sessionID {
		t.Fatalf("agentId = %q, want the session id %q", agentID, sessionID)
	}
	eventID, _ := stringField(t, waterfall, "eventId")
	if eventID != request.ID {
		t.Fatalf("eventId = %q, want %q so the answer still routes", eventID, request.ID)
	}
}

func TestEventsResultAllowsOnce(t *testing.T) {
	f := newFixture(t, Config{})
	runID := f.startRun(t, "allow")
	request := newApprovalRequest("approval-allow", runID)
	decisions, failures := pendingDecision(t, f.broker, request)
	<-f.broker.Requests()

	conn := f.mustDial(t)
	clientID := openEventsStream(t, conn, "events")
	waterfall := readItem(t, conn, "events")
	assertField(t, waterfall, "type", "waterfall")

	response := f.postResult(t, resultBody(t, clientID, request.ID, `{"kind":"result","value":"allowed-once"}`))
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.StatusCode)
	}
	envelope := decodeResponse(t, response)
	if !envelope.Result.OK {
		t.Fatalf("result.ok = false: %+v", envelope.Result.Error)
	}
	if envelope.Type != "server-response" || envelope.RPCID != "rpc-result" {
		t.Fatalf("envelope = %+v, want the echoed rpcId", envelope)
	}
	select {
	case decision := <-decisions:
		if decision.Action != approval.DecisionApprove {
			t.Fatalf("action = %q, want approve", decision.Action)
		}
		if decision.Scope != approval.ScopeOnce {
			t.Fatalf("scope = %q, want once", decision.Scope)
		}
		if decision.RequestID != request.ID {
			t.Fatalf("requestId = %q, want %q", decision.RequestID, request.ID)
		}
	case err := <-failures:
		t.Fatalf("broker request failed: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("broker did not resolve")
	}
}

func TestEventsResultRejects(t *testing.T) {
	f := newFixture(t, Config{})
	runID := f.startRun(t, "reject")
	request := newApprovalRequest("approval-reject", runID)
	decisions, failures := pendingDecision(t, f.broker, request)
	<-f.broker.Requests()

	conn := f.mustDial(t)
	clientID := openEventsStream(t, conn, "events")
	readItem(t, conn, "events")

	response := f.postResult(t, resultBody(t, clientID, request.ID, `{"kind":"result","value":"rejected"}`))
	envelope := decodeResponse(t, response)
	if !envelope.Result.OK {
		t.Fatalf("result.ok = false: %+v", envelope.Result.Error)
	}
	select {
	case decision := <-decisions:
		if decision.Action != approval.DecisionReject {
			t.Fatalf("action = %q, want reject", decision.Action)
		}
	case err := <-failures:
		t.Fatalf("broker request failed: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("broker did not resolve")
	}
}

func TestEventsResultUnknownEventFailsClosed(t *testing.T) {
	f := newFixture(t, Config{})
	conn := f.mustDial(t)
	clientID := openEventsStream(t, conn, "events")

	response := f.postResult(t, resultBody(t, clientID, "approval-does-not-exist", `{"kind":"result","value":"allowed-once"}`))
	envelope := decodeResponse(t, response)
	if envelope.Result.OK {
		t.Fatal("unknown eventId was accepted")
	}
	if envelope.Result.Error == nil || envelope.Result.Error.Code != codeApprovalNotFound {
		t.Fatalf("error = %+v, want code %q", envelope.Result.Error, codeApprovalNotFound)
	}
}

func TestEventsResultAlreadyAnsweredFailsClosed(t *testing.T) {
	f := newFixture(t, Config{})
	runID := f.startRun(t, "twice")
	request := newApprovalRequest("approval-twice", runID)
	decisions, _ := pendingDecision(t, f.broker, request)
	<-f.broker.Requests()

	conn := f.mustDial(t)
	clientID := openEventsStream(t, conn, "events")
	readItem(t, conn, "events")

	first := decodeResponse(t, f.postResult(t, resultBody(t, clientID, request.ID, `{"kind":"result","value":"allowed-once"}`)))
	if !first.Result.OK {
		t.Fatalf("first answer failed: %+v", first.Result.Error)
	}
	<-decisions

	second := decodeResponse(t, f.postResult(t, resultBody(t, clientID, request.ID, `{"kind":"result","value":"rejected"}`)))
	if second.Result.OK {
		t.Fatal("second answer to the same approval was accepted")
	}
	if second.Result.Error == nil || second.Result.Error.Code != codeApprovalNotFound {
		t.Fatalf("error = %+v, want code %q", second.Result.Error, codeApprovalNotFound)
	}
}

func TestEventsResultExpiredFailsClosed(t *testing.T) {
	f := newFixture(t, Config{})
	runID := f.startRun(t, "expired")
	request := newApprovalRequest("approval-expired", runID)
	request.CreatedAt = time.Now().UTC().Add(-2 * time.Hour)
	expired := time.Now().UTC().Add(-time.Hour)
	request.ExpiresAt = &expired
	decisions, failures := pendingDecision(t, f.broker, request)
	<-f.broker.Requests()

	conn := f.mustDial(t)
	clientID := openEventsStream(t, conn, "events")

	response := f.postResult(t, resultBody(t, clientID, request.ID, `{"kind":"result","value":"allowed-once"}`))
	envelope := decodeResponse(t, response)
	if envelope.Result.OK {
		t.Fatal("expired approval was accepted")
	}
	if envelope.Result.Error == nil || envelope.Result.Error.Code != codeApprovalConflict {
		t.Fatalf("error = %+v, want code %q", envelope.Result.Error, codeApprovalConflict)
	}
	// Fail closed means the request is still pending, not silently allowed.
	select {
	case decision := <-decisions:
		t.Fatalf("expired approval resolved with %+v", decision)
	case err := <-failures:
		t.Fatalf("expired approval resolved with error %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	if _, pending := f.broker.Pending(request.ID); !pending {
		t.Fatal("expired approval was removed from the pending table")
	}
}

func TestEventsResultUnknownClientFailsClosed(t *testing.T) {
	f := newFixture(t, Config{})
	runID := f.startRun(t, "client")
	request := newApprovalRequest("approval-client", runID)
	pendingDecision(t, f.broker, request)
	<-f.broker.Requests()

	response := f.postResult(t, resultBody(t, "not-a-client", request.ID, `{"kind":"result","value":"allowed-once"}`))
	envelope := decodeResponse(t, response)
	if envelope.Result.OK {
		t.Fatal("answer from an unknown clientId was accepted")
	}
	if envelope.Result.Error == nil || envelope.Result.Error.Code != codeArgumentsInvalid {
		t.Fatalf("error = %+v, want code %q", envelope.Result.Error, codeArgumentsInvalid)
	}
	if _, pending := f.broker.Pending(request.ID); !pending {
		t.Fatal("approval was resolved by an unidentified client")
	}
}

func TestEventsResultNextOutcomeFailsClosed(t *testing.T) {
	f := newFixture(t, Config{})
	runID := f.startRun(t, "next")
	request := newApprovalRequest("approval-next", runID)
	pendingDecision(t, f.broker, request)
	<-f.broker.Requests()

	conn := f.mustDial(t)
	clientID := openEventsStream(t, conn, "events")
	readItem(t, conn, "events")

	response := f.postResult(t, resultBody(t, clientID, request.ID, `{"kind":"next"}`))
	envelope := decodeResponse(t, response)
	if envelope.Result.OK {
		t.Fatal(`outcome "next" was accepted as an approval`)
	}
	if envelope.Result.Error == nil || envelope.Result.Error.Code != codeUnimplemented {
		t.Fatalf("error = %+v, want code %q", envelope.Result.Error, codeUnimplemented)
	}
	if _, pending := f.broker.Pending(request.ID); !pending {
		t.Fatal(`outcome "next" resolved the approval`)
	}
}

func TestEventsResultUnsupportedOutcomeValueFailsClosed(t *testing.T) {
	f := newFixture(t, Config{})
	runID := f.startRun(t, "cancelled")
	request := newApprovalRequest("approval-cancelled", runID)
	pendingDecision(t, f.broker, request)
	<-f.broker.Requests()

	conn := f.mustDial(t)
	clientID := openEventsStream(t, conn, "events")
	readItem(t, conn, "events")

	response := f.postResult(t, resultBody(t, clientID, request.ID, `{"kind":"result","value":"cancelled"}`))
	envelope := decodeResponse(t, response)
	if envelope.Result.OK {
		t.Fatal(`outcome value "cancelled" was accepted`)
	}
	if envelope.Result.Error == nil || envelope.Result.Error.Code != codeArgumentsInvalid {
		t.Fatalf("error = %+v, want code %q", envelope.Result.Error, codeArgumentsInvalid)
	}
	if _, pending := f.broker.Pending(request.ID); !pending {
		t.Fatal(`outcome value "cancelled" resolved the approval`)
	}
}

func TestEventsResultRejectsMethodMismatch(t *testing.T) {
	f := newFixture(t, Config{})
	body := `{"type":"client-request","rpcId":"rpc-1","method":"session/list","payload":{"args":{}}}`
	response := f.postResult(t, body)
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", response.StatusCode)
	}
}

func TestEventsResultRejectsMalformedEnvelope(t *testing.T) {
	f := newFixture(t, Config{})
	// These never reach the endpoint, so they are protocol errors rather than
	// result.ok:false envelopes.
	cases := map[string]string{
		"missing rpcId": `{"type":"client-request","method":"$events/result","payload":{"args":{}}}`,
		"missing args":  `{"type":"client-request","rpcId":"rpc-1","method":"$events/result","payload":{}}`,
		"missing type":  `{"rpcId":"rpc-1","method":"$events/result","payload":{"args":{}}}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			response := f.postResult(t, body)
			if response.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", response.StatusCode)
			}
		})
	}
}

func TestEventsResultRejectsNonObjectOutcome(t *testing.T) {
	f := newFixture(t, Config{})
	conn := f.mustDial(t)
	clientID := openEventsStream(t, conn, "events")

	response := f.postResult(t,
		resultBody(t, clientID, "approval-any", `"not-an-object"`))
	envelope := decodeResponse(t, response)
	if envelope.Result.OK {
		t.Fatal("a non-object outcome was accepted")
	}
	if envelope.Result.Error == nil || envelope.Result.Error.Code != codeArgumentsInvalid {
		t.Fatalf("error = %+v, want code %q", envelope.Result.Error, codeArgumentsInvalid)
	}
}

func TestEventsResultRejectsGet(t *testing.T) {
	f := newFixture(t, Config{})
	response, err := http.Get(f.server.URL + EventsResultPath)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", response.StatusCode)
	}
}

func TestMuxRejectsPost(t *testing.T) {
	f := newFixture(t, Config{})
	response, err := http.Post(f.server.URL+MuxPath, "application/json", nil)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", response.StatusCode)
	}
}

func TestHandlerUnknownPathIsNotFound(t *testing.T) {
	f := newFixture(t, Config{})
	response, err := http.Get(f.server.URL + "/api/other")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", response.StatusCode)
	}
}
