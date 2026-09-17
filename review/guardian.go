package review

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Mode decides what an adverse verdict does to the run.
type Mode string

const (
	// ModeOff disables the guardian.
	ModeOff Mode = "off"
	// ModeReport records the verdict and lets the run finish. It is the
	// default: a reviewer that can block is opted into, never assumed.
	ModeReport Mode = "report"
	// ModeEnforce turns a request_changes verdict into another turn, with
	// the findings as the agent's next instruction.
	ModeEnforce Mode = "enforce"
)

// ParseMode resolves a guardian mode.
func ParseMode(name string) (Mode, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", string(ModeOff):
		return ModeOff, nil
	case string(ModeReport), "on", "true":
		return ModeReport, nil
	case string(ModeEnforce), "block", "strict":
		return ModeEnforce, nil
	default:
		return "", fmt.Errorf("unknown review mode %q (want %s, %s, or %s)", name, ModeOff, ModeReport, ModeEnforce)
	}
}

// Guardian runs a reviewer over a finished run.
type Guardian struct {
	// Reviewer produces the verdict. Required for a configured guardian.
	Reviewer Reviewer
	// Mode decides whether an adverse verdict blocks the stop.
	Mode Mode
	// Timeout bounds one review. Zero uses DefaultTimeout.
	Timeout time.Duration
	// MaxFindings caps the findings kept from a verdict.
	MaxFindings int
	// FindingsBudget bounds the instruction rendered for the agent.
	FindingsBudget int
}

// Configured reports whether the guardian will review anything.
func (g *Guardian) Configured() bool {
	if g == nil || g.Reviewer == nil {
		return false
	}
	return g.Mode != ModeOff && g.Mode != ""
}

// Result is one review outcome, with the rendering the caller needs.
type Result struct {
	Verdict Verdict
	// Instruction is the text to hand the agent when the guardian
	// enforces the verdict; empty when the run may finish.
	Instruction string
	// Enforced reports that the verdict is being acted on.
	Enforced bool
}

// Review runs the reviewer and decides what the run must do. It returns
// done=false only when no review happened at all, which is how the caller
// distinguishes "not configured" from "reviewed".
func (g *Guardian) Review(ctx context.Context, request Request) (Result, bool) {
	if !g.Configured() {
		return Result{}, false
	}
	timeout := g.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	reviewCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	defer cancel()
	verdict, err := g.Reviewer.Review(reviewCtx, request)
	if err != nil {
		// A review that did not happen is not an approval: report the
		// failure and let the run finish, because a broken reviewer must
		// not hold the agent hostage.
		return Result{Verdict: Verdict{Summary: fmt.Sprintf("review failed: %v", err)}}, true
	}
	if g.MaxFindings > 0 && len(verdict.Findings) > g.MaxFindings {
		verdict.Findings = verdict.Findings[:g.MaxFindings]
	}
	result := Result{Verdict: verdict}
	if g.Mode == ModeEnforce && verdict.RequestsChanges() {
		result.Enforced = true
		result.Instruction = FormatFindings(verdict, g.FindingsBudget)
	}
	return result, true
}
