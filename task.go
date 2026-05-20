package zenforge

type Task struct {
	RunID string
	Input string
	Meta  map[string]any
}

type Result struct {
	RunID  string
	Output string
	Meta   map[string]any
}

