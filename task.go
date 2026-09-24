package zenforge

import (
	"github.com/feiyu912/zenforge/approval"
	"github.com/feiyu912/zenforge/model"
)

type Task struct {
	RunID           string
	Input           string
	InitialMessages []model.Message
	// Images are the images this turn's own prompt carries, delivered on the
	// user message the run starts with. They are separate from InitialMessages
	// because the run appends that message itself: a caller that put them in
	// InitialMessages would produce two consecutive user messages, which some
	// providers reject and the rest read in the wrong order. Empty for a run
	// started without images, such as a CLI run.
	Images []model.Image
	// Model, when set, is the adapter this run uses for every model call
	// instead of the route below. It is the most specific thing a caller can say
	// about a run's model, and it takes precedence so a caller that has already
	// built the adapter does not have it built a second time. It is not
	// serialized -- a model is a live client, and a run that arrives as JSON must
	// not be able to name one -- so a run that must survive a resume names the
	// route as well: only the route can be resolved again from a checkpoint.
	Model model.Model
	// ModelProvider and ModelName, when set, name the route this run's model
	// comes from. The run resolves the pair once through [Config.ModelResolver]
	// as it starts and keeps that adapter for every model call it makes, so a
	// concurrent run under another route -- or a host whose configured adapter
	// changed in between -- cannot move it. The pair is written into the run's
	// durable Meta, which is how a resume resolves the same route again; a route
	// that cannot be resolved again is a refused resume rather than a run on
	// another model. Empty for a run that uses the agent's configured adapter,
	// such as a CLI run.
	ModelProvider string
	ModelName     string
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
