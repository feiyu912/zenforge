package sandbox

const (
	MetadataStateKey      = "sandbox.state"
	MetadataClearStateKey = "sandbox.clearState"
)

type State struct {
	SessionID     string         `json:"sessionId"`
	RunID         string         `json:"runId"`
	SubtaskID     string         `json:"subtaskId,omitempty"`
	EnvironmentID string         `json:"environmentId"`
	WorkingDir    string         `json:"workingDir,omitempty"`
	Metadata      map[string]any `json:"metadata,omitempty"`
}

func StateFromMetadata(metadata map[string]any) (State, bool) {
	if metadata == nil {
		return State{}, false
	}
	value, ok := metadata[MetadataStateKey]
	if !ok {
		return State{}, false
	}
	switch state := value.(type) {
	case State:
		return state, state.SessionID != ""
	case *State:
		if state == nil {
			return State{}, false
		}
		return *state, state.SessionID != ""
	case map[string]any:
		out := State{
			SessionID:     stringValue(state["sessionId"]),
			RunID:         stringValue(state["runId"]),
			SubtaskID:     stringValue(state["subtaskId"]),
			EnvironmentID: stringValue(state["environmentId"]),
			WorkingDir:    stringValue(state["workingDir"]),
		}
		if meta, ok := state["metadata"].(map[string]any); ok {
			out.Metadata = cloneMap(meta)
		}
		return out, out.SessionID != ""
	default:
		return State{}, false
	}
}

func StateFromSession(session *Session) State {
	if session == nil {
		return State{}
	}
	return State{
		SessionID:     session.ID,
		RunID:         session.RunID,
		SubtaskID:     session.SubtaskID,
		EnvironmentID: session.EnvironmentID,
		WorkingDir:    session.WorkingDir,
		Metadata:      cloneMap(session.Metadata),
	}
}

func SessionFromState(state State, runID, subtaskID string) *Session {
	if state.SessionID == "" || state.RunID == "" || state.RunID != runID || state.SubtaskID != subtaskID {
		return nil
	}
	return &Session{
		ID:            state.SessionID,
		RunID:         runID,
		SubtaskID:     subtaskID,
		EnvironmentID: state.EnvironmentID,
		WorkingDir:    state.WorkingDir,
		Metadata:      cloneMap(state.Metadata),
	}
}

func cloneMap(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func stringValue(value any) string {
	out, _ := value.(string)
	return out
}
