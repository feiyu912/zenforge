package anthropic

type messagesRequest struct {
	Model      string           `json:"model"`
	MaxTokens  int              `json:"max_tokens"`
	System     string           `json:"system,omitempty"`
	Messages   []message        `json:"messages"`
	Tools      []toolDefinition `json:"tools,omitempty"`
	ToolChoice any              `json:"tool_choice,omitempty"`
	Stream     bool             `json:"stream"`
}

type message struct {
	Role    string         `json:"role"`
	Content []contentBlock `json:"content"`
}

type contentBlock struct {
	Type      string         `json:"type"`
	Text      string         `json:"text,omitempty"`
	ID        string         `json:"id,omitempty"`
	Name      string         `json:"name,omitempty"`
	Input     map[string]any `json:"input,omitempty"`
	ToolUseID string         `json:"tool_use_id,omitempty"`
	Content   string         `json:"content,omitempty"`
}

type toolDefinition struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"input_schema"`
}

type streamEvent struct {
	Type         string         `json:"type"`
	Index        int            `json:"index,omitempty"`
	ContentBlock contentBlock   `json:"content_block,omitempty"`
	Delta        streamDelta    `json:"delta,omitempty"`
	Usage        usage          `json:"usage,omitempty"`
	Message      messagePayload `json:"message,omitempty"`
}

type streamDelta struct {
	Type        string `json:"type"`
	Text        string `json:"text,omitempty"`
	PartialJSON string `json:"partial_json,omitempty"`
}

type messagePayload struct {
	Usage usage `json:"usage,omitempty"`
}

type usage struct {
	InputTokens  int `json:"input_tokens,omitempty"`
	OutputTokens int `json:"output_tokens,omitempty"`
}
