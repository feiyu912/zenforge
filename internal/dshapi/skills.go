package dshapi

import (
	"context"
	"encoding/json"
	"fmt"
)

// SkillInfo is one row of the console's skills panel: what the skill is called,
// what it does, the routing guidance it declares, and whether the model may pick
// it up on its own. The reference sends exactly these four fields (its own row
// also allows an optional path, which it does not populate and this host does not
// either).
type SkillInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	// WhenToUse is the skill's own extra guidance, omitted when it declares none.
	WhenToUse string `json:"whenToUse,omitempty"`
	// ModelInvocable is false for a skill that declares itself hidden from the
	// model: the panel still lists it for the operator, with the badge that says
	// the model will not discover it.
	ModelInvocable bool `json:"modelInvocable"`
}

// SkillSource lists the skills the console may show. The read is per request
// rather than a startup snapshot because a skill catalog is a directory: a
// package dropped in while the host runs should appear without a restart.
type SkillSource interface {
	Skills(ctx context.Context) ([]SkillInfo, error)
}

// SetSkills installs the catalog the skills methods answer from.
func (h *Handler) SetSkills(source SkillSource) {
	h.skillsMu.Lock()
	h.skills = source
	h.skillsMu.Unlock()
}

// skillsSource is the installed catalog, or nil when none is.
func (h *Handler) skillsSource() SkillSource {
	h.skillsMu.RLock()
	defer h.skillsMu.RUnlock()
	return h.skills
}

// sessionSkills answers POST /api/skills/list. The session is validated first --
// the reference resolves a session's composition before reading any registry, and
// an unknown id answers session/not-found -- and the answer is always an array: a
// host with no skills installed answers an empty panel, which is a fact, where
// null would read as a failure.
func (h *Handler) sessionSkills(ctx context.Context, args map[string]json.RawMessage) (any, *methodError) {
	if failure := rejectUnknownArguments(args, "sessionId"); failure != nil {
		return nil, failure
	}
	sessionID, _, failure := stringArg(args, "sessionId")
	if failure != nil {
		return nil, failure
	}
	if sessionID == "" {
		return nil, argumentRequired("sessionId")
	}
	if !h.sessionKnown(ctx, sessionID) {
		return nil, fail(codeSessionNotFound, fmt.Sprintf("session %q not found", sessionID),
			map[string]any{"sessionId": sessionID})
	}
	source := h.skillsSource()
	if source == nil {
		return nil, skillsDependencyMissing("skills/list")
	}
	rows, err := source.Skills(ctx)
	if err != nil {
		// The reference's own sentence for a registry that failed mid-read.
		return nil, fail(codeInternal, "skill listing failed: "+err.Error(), nil)
	}
	if rows == nil {
		rows = []SkillInfo{}
	}
	return map[string]any{"skills": rows}, nil
}

// skillsDependencyMissing reports a host that was started without a skill
// catalog, naming the seam serve installs.
func skillsDependencyMissing(method string) *methodError {
	return fail(codeUnimplemented,
		method+" is not configured: the host has no skill catalog; the serve command must install one with Handler.SetSkills",
		map[string]any{"dependency": "SkillSource"})
}
