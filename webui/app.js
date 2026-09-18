// Console for `zenforge serve`. Plain ES2020, no framework and no build step:
// the server ships this file verbatim.
//
// The page talks to four surfaces on the same origin:
//   GET  /api/settings   current model settings (never the key)
//   POST /api/settings   apply model settings for later runs
//   GET  /api/server     read-only server facts such as the workspace
//   GET  /runs           run list        GET /runs/attach  SSE transcript
//   POST /runs/start     start a run     POST /runs/cancel stop one
//   GET  /approvals      pending approvals, POST /approval to decide
"use strict";

const STATUS_LABELS = {
  starting: "starting",
  running: "running",
  waiting_approval: "waiting approval",
  completed: "completed",
  failed: "failed",
  cancelled: "cancelled",
};

// Named SSE events the transcript renders. EventSource only delivers named
// events to a matching addEventListener, so the list is explicit; an event
// type not listed here is simply not shown rather than silently mis-rendered.
const SSE_EVENTS = [
  "run.started",
  "run.resumed",
  "step.started",
  "model.delta",
  "model.reasoning",
  "tool.call",
  "tool.result",
  "tool.error",
  "approval.requested",
  "approval.resolved",
  "approval.expired",
  "workspace.changed",
  "run.done",
  "run.error",
  "run.cancelled",
];

const TERMINAL_STATUSES = ["completed", "failed", "cancelled"];

const state = {
  runs: [],
  selectedRunId: null,
  stream: null,
  toolCards: new Map(),
  approvalCards: new Map(),
  assistant: null,
  pollOk: false,
  hasApiKey: false,
};

const $ = (id) => document.getElementById(id);

async function api(path, options) {
  const response = await fetch(path, options);
  const text = await response.text();
  let body = null;
  if (text) {
    try {
      body = JSON.parse(text);
    } catch (err) {
      throw new Error("server returned non-JSON response");
    }
  }
  if (!response.ok) {
    const message = body && body.error && body.error.message ? body.error.message : response.statusText;
    throw new Error(message || ("request failed with status " + response.status));
  }
  return body;
}

function element(tag, className, text) {
  const node = document.createElement(tag);
  if (className) {
    node.className = className;
  }
  if (text !== undefined && text !== null) {
    node.textContent = text;
  }
  return node;
}

function formatTime(value) {
  if (!value) {
    return "";
  }
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) {
    return "";
  }
  return date.toLocaleTimeString();
}

function shortId(runId) {
  if (!runId) {
    return "";
  }
  return runId.length > 16 ? runId.slice(0, 13) + "…" : runId;
}

function statusLabel(status) {
  return STATUS_LABELS[status] || status || "unknown";
}

function isTerminal(status) {
  return TERMINAL_STATUSES.indexOf(status) >= 0;
}

// ---------------------------------------------------------------- run list

async function refreshRuns() {
  try {
    const body = await api("/runs");
    state.runs = (body && body.runs) || [];
    setLive(true);
    renderRuns();
  } catch (err) {
    setLive(false);
  }
}

function setLive(ok) {
  state.pollOk = ok;
  const node = $("live");
  node.classList.toggle("live-ok", ok);
  node.classList.toggle("live-unknown", !ok);
  node.textContent = ok ? "live" : "offline";
}

function renderRuns() {
  const list = $("run-list");
  list.textContent = "";
  $("run-list-empty").hidden = state.runs.length > 0;

  for (const run of state.runs) {
    const item = element("li", "run-item");
    if (run.runId === state.selectedRunId) {
      item.classList.add("selected");
    }
    item.addEventListener("click", () => selectRun(run.runId));

    const top = element("div", "run-item-top");
    top.appendChild(element("span", "run-item-id", shortId(run.runId)));
    top.appendChild(element("span", "badge badge-" + (run.status || "unknown"), statusLabel(run.status)));
    item.appendChild(top);

    const meta = element("div", "run-item-meta");
    meta.appendChild(element("span", null, formatTime(run.updatedAt || run.startedAt)));
    if (run.error) {
      meta.appendChild(element("span", "run-item-error", "error"));
    }
    item.appendChild(meta);

    list.appendChild(item);
  }
  updateHeader();
}

// --------------------------------------------------------------- transcript

function transcript() {
  return $("transcript");
}

function clearTranscript() {
  const node = transcript();
  node.textContent = "";
  state.toolCards.clear();
  state.approvalCards.clear();
  state.assistant = null;
}

function appendAssistantDelta(text) {
  if (!state.assistant) {
    state.assistant = element("div", "message assistant");
    transcript().appendChild(state.assistant);
  }
  state.assistant.textContent += text;
  scrollTranscript();
}

function endAssistant() {
  state.assistant = null;
}

function appendSystem(text, className) {
  transcript().appendChild(element("div", "system " + (className || ""), text));
  scrollTranscript();
}

function appendToolCall(data) {
  endAssistant();
  const details = element("details", "tool");
  const summary = element("summary");
  summary.appendChild(element("span", "tool-name", data.toolName || "tool"));
  summary.appendChild(element("span", "tool-id muted", shortId(data.toolCallId || "")));
  details.appendChild(summary);

  if (data.arguments !== undefined && data.arguments !== null) {
    details.appendChild(element("pre", "tool-args", pretty(data.arguments)));
  }
  const body = element("div", "tool-body");
  details.appendChild(body);
  transcript().appendChild(details);
  if (data.toolCallId) {
    state.toolCards.set(data.toolCallId, { details, body });
  }
  scrollTranscript();
}

function appendToolResult(data, failed) {
  const card = data.toolCallId ? state.toolCards.get(data.toolCallId) : null;
  const target = card ? card.body : transcript();
  if (failed) {
    target.appendChild(element("div", "error", data.error || "tool failed"));
  }
  const output = data.output;
  if (output !== undefined && output !== null && output !== "") {
    target.appendChild(element("pre", "tool-output", pretty(output)));
  }
  if (card && failed) {
    card.details.classList.add("tool-failed");
  }
  scrollTranscript();
}

function appendApproval(data) {
  endAssistant();
  const requestId = data.requestId || "";
  const card = element("div", "approval-card");
  card.appendChild(element("h3", null, "approval required: " + (data.operation || data.toolName || "action")));
  if (data.risk) {
    card.appendChild(element("p", "muted", "risk: " + data.risk));
  }
  const request = data.request || {};
  if (request.title) {
    card.appendChild(element("p", null, request.title));
  }
  if (request.description) {
    card.appendChild(element("p", "muted", request.description));
  }

  const actions = element("div", "approval-actions");
  const approve = element("button", "primary", "approve");
  const reject = element("button", "danger", "reject");
  approve.addEventListener("click", () => decideApproval(requestId, "approve", actions));
  reject.addEventListener("click", () => decideApproval(requestId, "reject", actions));
  actions.appendChild(approve);
  actions.appendChild(reject);
  card.appendChild(actions);

  transcript().appendChild(card);
  if (requestId) {
    state.approvalCards.set(requestId, card);
  }
  scrollTranscript();
}

async function decideApproval(requestId, action, actions) {
  if (!requestId) {
    return;
  }
  actions.textContent = "submitting…";
  try {
    await api("/approval", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ requestId: requestId, action: action, scope: "once" }),
    });
    actions.textContent = action === "approve" ? "approved" : "rejected";
  } catch (err) {
    actions.textContent = "";
    actions.appendChild(element("span", "error", err.message));
  }
}

function pretty(value) {
  if (typeof value === "string") {
    return value;
  }
  try {
    return JSON.stringify(value, null, 2);
  } catch (err) {
    return String(value);
  }
}

function scrollTranscript() {
  const node = transcript();
  node.scrollTop = node.scrollHeight;
}

// ------------------------------------------------------------------ stream

function closeStream() {
  if (state.stream) {
    state.stream.close();
    state.stream = null;
  }
}

function selectRun(runId) {
  if (!runId) {
    return;
  }
  state.selectedRunId = runId;
  closeStream();
  clearTranscript();
  updateHeader();
  renderRuns();

  const stream = new EventSource("/runs/attach?runId=" + encodeURIComponent(runId));
  state.stream = stream;
  for (const type of SSE_EVENTS) {
    stream.addEventListener(type, (message) => handleEvent(type, message));
  }
  stream.addEventListener("open", () => setLive(true));
  stream.addEventListener("error", () => {
    // A closed stream is the normal end for a terminal run; for a live run
    // EventSource reconnects on its own, so the indicator only dims.
    setLive(false);
  });
}

function handleEvent(type, message) {
  let data = {};
  if (message.data) {
    try {
      data = JSON.parse(message.data);
    } catch (err) {
      return;
    }
  }
  switch (type) {
    case "model.delta":
      appendAssistantDelta(data.textDelta || "");
      break;
    case "model.reasoning":
      // Reasoning is rendered as a dim line, never merged into the answer.
      appendSystem(data.textDelta || "", "reasoning");
      break;
    case "tool.call":
      appendToolCall(data);
      break;
    case "tool.result":
      appendToolResult(data, false);
      break;
    case "tool.error":
      appendToolResult(data, true);
      break;
    case "approval.requested":
      appendApproval(data);
      break;
    case "approval.resolved":
    case "approval.expired":
      resolveApprovalCard(data);
      break;
    case "run.started":
    case "run.resumed":
      appendSystem("run " + type.split(".")[1]);
      break;
    case "step.started":
      if (typeof data.step !== "undefined") {
        appendSystem("step " + data.step, "step");
      }
      break;
    case "workspace.changed":
      if (data.path) {
        appendSystem("changed " + data.path, "step");
      }
      break;
    case "run.done":
      endAssistant();
      appendSystem("run completed");
      finishRun();
      break;
    case "run.error":
      endAssistant();
      appendSystem("run failed: " + (data.error || "unknown error"), "error-block");
      finishRun();
      break;
    case "run.cancelled":
      endAssistant();
      appendSystem("run cancelled");
      finishRun();
      break;
    default:
      break;
  }
  updateHeaderFromEvent(type, data);
}

function resolveApprovalCard(data) {
  const card = data.requestId ? state.approvalCards.get(data.requestId) : null;
  if (!card) {
    return;
  }
  card.classList.add("resolved");
  const actions = card.querySelector(".approval-actions");
  if (actions) {
    actions.textContent = data.action ? ("decision: " + data.action) : "decided";
  }
}

function finishRun() {
  closeStream();
  refreshRuns();
}

function updateHeaderFromEvent(type, data) {
  if (type === "approval.requested") {
    setHeaderStatus("waiting_approval");
  } else if (type === "approval.resolved" || type === "approval.expired") {
    setHeaderStatus("running");
  } else if (type === "run.done") {
    setHeaderStatus("completed");
  } else if (type === "run.error") {
    setHeaderStatus("failed");
  } else if (type === "run.cancelled") {
    setHeaderStatus("cancelled");
  } else if (type === "step.started" && typeof data.step !== "undefined") {
    $("run-status").dataset.step = String(data.step);
  }
}

// ------------------------------------------------------------------ header

function currentRun() {
  return state.runs.find((run) => run.runId === state.selectedRunId) || null;
}

function updateHeader() {
  const run = currentRun();
  if (!run) {
    $("run-title").textContent = state.selectedRunId ? shortId(state.selectedRunId) : "no run selected";
    $("run-status").textContent = "idle";
    $("run-status").className = "badge badge-idle";
    $("cancel").disabled = true;
    return;
  }
  $("run-title").textContent = run.runId;
  setHeaderStatus(run.status);
  $("cancel").disabled = isTerminal(run.status);
}

function setHeaderStatus(status) {
  const node = $("run-status");
  node.textContent = statusLabel(status);
  node.className = "badge badge-" + (status || "unknown");
  $("cancel").disabled = isTerminal(status);
}

// ---------------------------------------------------------------- composer

async function startRun() {
  const input = $("task").value.trim();
  if (!input) {
    showComposerHint("enter a task first", true);
    return;
  }
  const button = $("start");
  button.disabled = true;
  try {
    const info = await api("/runs/start", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ input: input }),
    });
    $("task").value = "";
    showComposerHint("");
    await refreshRuns();
    selectRun(info.runId);
  } catch (err) {
    showComposerHint(err.message, true);
  } finally {
    button.disabled = false;
  }
}

async function cancelRun() {
  const runId = state.selectedRunId;
  if (!runId) {
    return;
  }
  try {
    await api("/runs/cancel", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ runId: runId }),
    });
  } catch (err) {
    showComposerHint(err.message, true);
    return;
  }
  refreshRuns();
}

function showComposerHint(text, isError) {
  const node = $("composer-hint");
  node.textContent = text || "";
  node.className = isError ? "error" : "muted";
}

// ---------------------------------------------------------------- settings

async function loadSettings() {
  try {
    const settings = await api("/api/settings");
    applySettingsToForm(settings);
  } catch (err) {
    showSettingsError(err.message);
  }
}

function applySettingsToForm(settings) {
  state.hasApiKey = Boolean(settings.hasApiKey);
  $("settings-base-url").value = settings.baseUrl || "";
  $("settings-model").value = settings.model || "";
  $("settings-provider").value = settings.provider === "anthropic" ? "anthropic" : "openai";
  $("settings-api-key").value = "";
  $("settings-api-key").placeholder = state.hasApiKey ? "set — paste a new key to replace" : "not set";
  $("settings-key-note").textContent = state.hasApiKey
    ? "A key is set. Saving without a new key keeps it; the key itself is never shown again."
    : "No key is set. Runs will fail until one is saved (or the server's env var provides it).";
  updateComposerHint();
}

function updateComposerHint() {
  if (!state.hasApiKey) {
    showComposerHint("no API key set — open settings (⚙) to add one before running", true);
  } else {
    showComposerHint("");
  }
}

async function saveSettings() {
  const button = $("settings-save");
  button.disabled = true;
  showSettingsError("");
  try {
    // Read the fields inside the try: a missing element used to throw before
    // the request was made, which left the button disabled with no message.
    const payload = {
      baseUrl: $("settings-base-url").value.trim(),
      model: $("settings-model").value.trim(),
      provider: $("settings-provider").value,
      apiKey: $("settings-api-key").value,
    };
    const settings = await api("/api/settings", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(payload),
    });
    applySettingsToForm(settings);
    $("settings-key-note").textContent =
      "Saved. The key stays in this server process: it is never returned by the API, logged, or written to disk.";
  } catch (err) {
    showSettingsError(err.message);
  } finally {
    button.disabled = false;
  }
}

function showSettingsError(message) {
  const node = $("settings-error");
  node.textContent = message || "";
  node.hidden = !message;
}

function openSettings() {
  $("settings-overlay").hidden = false;
  loadSettings();
}

function closeSettings() {
  $("settings-overlay").hidden = true;
}

// ------------------------------------------------------------------ server

async function loadServerInfo() {
  try {
    const info = await api("/api/server");
    const parts = [];
    if (info.workspace) {
      $("workspace").value = info.workspace;
      parts.push(info.workspace);
    }
    $("server-info").textContent = parts.join(" · ");
    $("server-info").title = "workspace: " + (info.workspace || "unknown");
  } catch (err) {
    $("server-info").textContent = "server info unavailable";
  }
}

// -------------------------------------------------------------------- init

function bind() {
  $("start").addEventListener("click", startRun);
  $("cancel").addEventListener("click", cancelRun);
  $("refresh").addEventListener("click", refreshRuns);
  $("settings-open").addEventListener("click", openSettings);
  $("settings-close").addEventListener("click", closeSettings);
  $("settings-save").addEventListener("click", saveSettings);
  $("settings-overlay").addEventListener("click", (event) => {
    if (event.target === $("settings-overlay")) {
      closeSettings();
    }
  });
  $("task").addEventListener("keydown", (event) => {
    if ((event.metaKey || event.ctrlKey) && event.key === "Enter") {
      startRun();
    }
  });
}

async function init() {
  bind();
  await loadServerInfo();
  await loadSettings();
  await refreshRuns();
  setInterval(refreshRuns, 2000);
}

init();