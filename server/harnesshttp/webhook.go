package harnesshttp

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/feiyu912/zenforge"
)

// WebhookRunPath is the fixed route for the signed-webhook run trigger.
// RegisterWebhookRun installs it only when a secret is configured.
const WebhookRunPath = "/webhook/run"

// Headers carrying the shared-secret signature and the signing timestamp.
const (
	WebhookSignatureHeader = "X-ZenForge-Signature"
	WebhookTimestampHeader = "X-ZenForge-Timestamp"
)

// The accepted timestamp window is deliberately asymmetric. A request may be a
// few minutes late — sender clock drift, a retry, or a queue between the sender
// and this server — but it must not come from far in the future, where a wide
// window would let a captured signature stay usable for that whole window.
const (
	webhookMaxAge        = 5 * time.Minute
	webhookMaxFutureSkew = time.Minute
	// The body is read before its signature can be checked, so cap it here to
	// keep an unauthenticated sender from forcing an unbounded allocation.
	maxWebhookBodyBytes = 1 << 20
)

var (
	errWebhookMissingSignature = errors.New("missing signature")
	errWebhookInvalidSignature = errors.New("invalid signature")
	errWebhookMissingTimestamp = errors.New("missing timestamp")
	errWebhookInvalidTimestamp = errors.New("invalid timestamp")
	errWebhookExpiredTimestamp = errors.New("timestamp outside the accepted window")
)

// WebhookOptions configures the signed-webhook run trigger. An empty Secret
// disables the endpoint: an unauthenticated run trigger must not exist by
// accident, so there is no "empty secret means allow everything" mode. Now is
// injectable so the replay-window tests need no sleeps.
type WebhookOptions struct {
	Secret string
	Now    func() time.Time
}

// WebhookRunRequest is the JSON body accepted by ServeWebhookRun. Metadata is
// passed through as run task metadata; the manager already carries it.
type WebhookRunRequest struct {
	Prompt   string         `json:"prompt"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

// WebhookRunResponse acknowledges a queued webhook trigger. The run manager's
// own status ("starting") is authoritative for execution; the reply says
// "queued" because the caller's next step is to poll runId, not because a
// separate queue exists.
type WebhookRunResponse struct {
	RunID  string `json:"runId"`
	Status string `json:"status"`
}

// RegisterWebhookRun adds the signed-webhook trigger to mux.
//
// Registration itself is the guard: with no secret the route is simply absent,
// so a misconfigured deployment answers 404 instead of exposing a trigger that
// starts runs without authentication.
func (h *Handler) RegisterWebhookRun(mux *http.ServeMux) {
	if h == nil || mux == nil {
		return
	}
	if strings.TrimSpace(h.Webhook.Secret) == "" {
		return
	}
	mux.HandleFunc(WebhookRunPath, h.ServeWebhookRun)
}

// ServeWebhookRun authenticates a signed webhook call and starts a detached run
// through the existing manager path.
func (h *Handler) ServeWebhookRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "webhook run requires POST")
		return
	}
	// Fail closed when the handler is reached without a secret, even if an
	// application forgot to use RegisterWebhookRun.
	if strings.TrimSpace(h.Webhook.Secret) == "" {
		writeError(w, http.StatusNotFound, "webhook_not_configured", "webhook endpoint is not configured")
		return
	}
	if h.Manager == nil {
		writeError(w, http.StatusServiceUnavailable, "manager_not_configured", "run manager is not configured")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxWebhookBodyBytes))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", err.Error())
		return
	}
	// Authenticate before parsing: a body that is not signed is not worth
	// decoding. The error text is fixed so neither the secret nor the supplied
	// signature can leak through the response.
	if err := verifyWebhookRequest(h.Webhook, r, body); err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", err.Error())
		return
	}
	var req WebhookRunRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	req.Prompt = strings.TrimSpace(req.Prompt)
	if req.Prompt == "" {
		writeError(w, http.StatusBadRequest, "prompt_required", "prompt is required")
		return
	}
	decision, ok := h.authorize(w, r, Operation{Name: "webhookRun"})
	if !ok {
		return
	}
	info, err := h.Manager.Start(r.Context(), zenforge.Task{
		Input:             req.Prompt,
		Meta:              mergeMeta(req.Metadata, decision.Meta),
		ApprovalNamespace: decision.ApprovalNamespace,
	})
	if err != nil {
		writeManagerError(w, "webhook_run_failed", err)
		return
	}
	writeJSON(w, http.StatusAccepted, WebhookRunResponse{
		RunID:  info.RunID,
		Status: "queued",
	})
}

// verifyWebhookRequest checks the shared-secret signature and its timestamp.
//
// The signature covers "<timestamp>.<raw body>", not the body alone. Binding
// the timestamp into the MAC is what makes the freshness window meaningful: a
// captured signature cannot be re-presented with a different timestamp,
// because the timestamp the attacker would have to change is part of what was
// signed. The signature is compared with hmac.Equal so the check does not leak
// the expected value through timing.
func verifyWebhookRequest(opts WebhookOptions, r *http.Request, body []byte) error {
	provided := strings.TrimSpace(r.Header.Get(WebhookSignatureHeader))
	if provided == "" {
		return errWebhookMissingSignature
	}
	// Accept the documented "sha256=<hex>" form and a bare <hex>.
	provided = strings.TrimPrefix(provided, "sha256=")
	providedMAC, err := hex.DecodeString(provided)
	if err != nil {
		return errWebhookInvalidSignature
	}
	rawTimestamp := strings.TrimSpace(r.Header.Get(WebhookTimestampHeader))
	if rawTimestamp == "" {
		return errWebhookMissingTimestamp
	}
	seconds, err := strconv.ParseInt(rawTimestamp, 10, 64)
	if err != nil {
		return errWebhookInvalidTimestamp
	}
	now := time.Now
	if opts.Now != nil {
		now = opts.Now
	}
	at := time.Unix(seconds, 0)
	if at.Before(now().Add(-webhookMaxAge)) || at.After(now().Add(webhookMaxFutureSkew)) {
		return errWebhookExpiredTimestamp
	}
	mac := hmac.New(sha256.New, []byte(opts.Secret))
	mac.Write([]byte(rawTimestamp))
	mac.Write([]byte("."))
	mac.Write(body)
	if !hmac.Equal(providedMAC, mac.Sum(nil)) {
		return errWebhookInvalidSignature
	}
	return nil
}
