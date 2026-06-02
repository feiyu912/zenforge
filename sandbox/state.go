package sandbox

type State struct {
	SessionID     string         `json:"sessionId"`
	EnvironmentID string         `json:"environmentId"`
	WorkingDir    string         `json:"workingDir,omitempty"`
	Metadata      map[string]any `json:"metadata,omitempty"`
}

func StateFromSession(session *Session) State {
	if session == nil {
		return State{}
	}
	return State{
		SessionID:     session.ID,
		EnvironmentID: session.EnvironmentID,
		WorkingDir:    session.WorkingDir,
		Metadata:      cloneMap(session.Metadata),
	}
}

func SessionFromState(state State, runID, subtaskID string) *Session {
	if state.SessionID == "" {
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
