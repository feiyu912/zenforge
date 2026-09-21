package zenforge

import (
	"github.com/feiyu912/zenforge/approval"
	"github.com/feiyu912/zenforge/model"
)

type Task struct {
	RunID           string
	Input           string
	InitialMessages []model.Message
	// PromptID is the caller's identity for the prompt that starts this run --
	// the console's `requestId`, which it also sends on the prompt RPC and uses
	// to match the durable message with the submission it echoed locally. It is
	// checkpointed with the run and emitted as run.started's "promptId", so a
	// host projecting the run can carry the identity on the projected user
	// message. Empty for a run started without one, such as a CLI run or a
	// resumed run, and the identity is then simply absent.
	PromptID          string
	Meta              map[string]any
	ApprovalNamespace approval.Namespace
	// OnEvent, when set, is called for every event [Agent.Run] consumes, in
	// order and before the run returns. It exists so a caller that needs to
	// narrate a run to someone else (an MCP client watching a served run)
	// can do so from the single loop that already drains the stream, instead
	// of opening a second subscription to the same events.
	//
	// It is called on the goroutine running the task, synchronously: an
	// implementation must be quick and must not block on anything the run
	// itself is waiting for, or it stalls the run it is observing.
	OnEvent func(Event)
}

type Result struct {
	RunID  string
	Output string
	Meta   map[string]any
}
