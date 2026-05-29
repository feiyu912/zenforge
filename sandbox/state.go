package sandbox

type State struct {
	SessionID     string         `json:"sessionId"`
	EnvironmentID string         `json:"environmentId"`
	WorkingDir    string         `json:"workingDir,omitempty"`
	Metadata      map[string]any `json:"metadata,omitempty"`
}
