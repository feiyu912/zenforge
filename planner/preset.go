package planner

const (
	// PlanPrompt is appended to the plan-execute run's input. It asks for the plan
	// the preset is named for, but only when the request needs one: forcing a plan
	// for a question produced a task list and a plan stage with nothing to execute
	// (ADR 0107).
	PlanPrompt = `Decide whether this request needs a plan. If it needs more than one step, call todo_write with a concise todo list now, before doing anything else, and do not give a final answer until that plan exists. If it is a question or a single-step request, answer it directly and do not create a plan.`

	TaskPromptTemplate = `Task list:
{{todo_list}}

Current task ID: {{todo_id}}
Current task:
{{todo_content}}

Rules:
1. Work only on the current task.
2. Use tools as needed.
3. Before finishing this task, call todo_update with done, failed, or cancelled.`

	SummaryPromptTemplate = `Original request:
{{input}}

Final todo list:
{{todo_list}}

Provide the final user-facing summary.`
)
