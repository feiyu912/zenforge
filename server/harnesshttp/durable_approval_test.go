package harnesshttp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/feiyu912/zenforge/approval"
	approvalmemory "github.com/feiyu912/zenforge/approval/memory"
)

func TestDurableApprovalHTTPCommitsBeforeWaiterConsumes(t *testing.T) {
	store := approvalmemory.NewStore()
	waiter := newApprovalStoreBroker(t, store)
	submitter := newApprovalStoreBroker(t, store)
	request := approval.Request{
		ID: "durable_approval", RunID: "run_durable", Operation: "shell.command",
		Title: "Run command", Risk: approval.RiskHigh,
		Options: approval.DefaultOptions(), CreatedAt: time.Now().UTC(),
	}
	if err := waiter.RegisterRequest(context.Background(), request); err != nil {
		t.Fatal(err)
	}

	handler := &Handler{ApprovalInbox: submitter}
	list := httptest.NewRecorder()
	handler.ServeApprovals(list, httptest.NewRequest(
		http.MethodGet, "/approvals?runId=run_durable", nil,
	))
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), request.ID) {
		t.Fatalf("list status=%d body=%s", list.Code, list.Body.String())
	}

	body := `{"requestId":"durable_approval","action":"approve","scope":"once"}`
	submit := httptest.NewRecorder()
	handler.ServeApproval(submit, httptest.NewRequest(http.MethodPost, "/approval", strings.NewReader(body)))
	if submit.Code != http.StatusOK {
		t.Fatalf("submit status=%d body=%s", submit.Code, submit.Body.String())
	}
	record, err := store.Get(context.Background(), request.ID)
	if err != nil || record.Status != approval.StatusResolved || record.Decision == nil {
		t.Fatalf("committed record = (%+v, %v)", record, err)
	}

	decision, err := waiter.Request(context.Background(), request)
	if err != nil || decision.Action != approval.DecisionApprove {
		t.Fatalf("later waiter consumed = (%+v, %v)", decision, err)
	}

	retry := httptest.NewRecorder()
	handler.ServeApproval(retry, httptest.NewRequest(http.MethodPost, "/approval", strings.NewReader(body)))
	if retry.Code != http.StatusOK {
		t.Fatalf("idempotent retry status=%d body=%s", retry.Code, retry.Body.String())
	}
	conflict := httptest.NewRecorder()
	handler.ServeApproval(conflict, httptest.NewRequest(
		http.MethodPost,
		"/approval",
		strings.NewReader(`{"requestId":"durable_approval","action":"reject","scope":"once"}`),
	))
	if conflict.Code != http.StatusConflict {
		t.Fatalf("conflict status=%d body=%s", conflict.Code, conflict.Body.String())
	}
}

func TestDurableApprovalHTTPAuthorizesStoredRun(t *testing.T) {
	store := approvalmemory.NewStore()
	inbox := newApprovalStoreBroker(t, store)
	request := approval.Request{
		ID: "authorized_approval", RunID: "stored_run", Operation: "write",
		Title: "Write", Risk: approval.RiskMedium,
		Options: approval.DefaultOptions(), CreatedAt: time.Now().UTC(),
	}
	if err := inbox.RegisterRequest(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	var authorized Operation
	handler := &Handler{
		ApprovalInbox: inbox,
		Access: AccessFunc(func(_ context.Context, _ *http.Request, operation Operation) (AccessDecision, error) {
			authorized = operation
			return AccessDecision{}, nil
		}),
	}
	rec := httptest.NewRecorder()
	handler.ServeApproval(rec, httptest.NewRequest(
		http.MethodPost,
		"/approval",
		strings.NewReader(`{"requestId":"authorized_approval","action":"approve"}`),
	))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if authorized != (Operation{Name: "approval", RunID: "stored_run"}) {
		t.Fatalf("authorized operation = %+v", authorized)
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
}

func TestApprovalHandlerTreatsTypedNilInboxAsUnconfigured(t *testing.T) {
	var inbox *approval.StoreBroker
	handler := &Handler{ApprovalInbox: inbox}
	rec := httptest.NewRecorder()
	handler.ServeApprovals(rec, httptest.NewRequest(http.MethodGet, "/approvals?runId=run", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rec.Code, rec.Body.String())
	}
}

func newApprovalStoreBroker(t *testing.T, store approval.PendingStore) *approval.StoreBroker {
	t.Helper()
	broker, err := approval.NewStoreBroker(store, approval.StoreBrokerOptions{PollInterval: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	return broker
}
