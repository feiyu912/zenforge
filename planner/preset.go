package planner

const (
	PlanPrompt = `Create a concise todo plan for the user's request. You must call todo_write with the todo list before giving any final answer.`

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
