package zenforge

import (
	"github.com/feiyu912/zenforge/approval"
	"github.com/feiyu912/zenforge/model"
)

type Task struct {
	RunID             string
	Input             string
	InitialMessages   []model.Message
	Meta              map[string]any
	ApprovalNamespace approval.Namespace
}

type Result struct {
	RunID  string
	Output string
	Meta   map[string]any
}
