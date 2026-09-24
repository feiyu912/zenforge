package harness

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
)

// resultLabel is the word the table shows for a cell. It is deliberately not a
// boolean: `unsupported` is neither success nor failure, and collapsing it into
// either would lose the one thing the cell is there to say.
func resultLabel(cell Cell) string {
	switch cell.Status {
	case "completed":
		return "success"
	case "unsupported":
		return "unsupported"
	case "unavailable":
		return "unavailable"
	default:
		return "failed"
	}
}

// printCell writes the one-line summary of a finished run, with the failures
// that decided it. The table comes later; this line exists so a long run shows
// progress where a reader is already looking.
func printCell(out io.Writer, cell Cell) {
	if cell.Samples > 1 {
		// A median with one sample is just the sample; saying so is the
		// difference between a number and a measurement.
		fmt.Fprintf(out, "run %s/%s: %s n=%d wall_median=%dms overhead_median=%dms min=%dms max=%dms requests=%d prompt=%dB tools=%dB recovery=%s\n",
			cell.Task, cell.Runner, resultLabel(cell), cell.Samples,
			cell.WallMillis, cell.OverheadMillis, cell.MinWallMillis, cell.MaxWallMillis,
			cell.Requests, cell.PromptBytes, cell.ToolSchemaBytes, cell.Recovery)
	} else {
		fmt.Fprintf(out, "run %s/%s: %s wall=%dms model=%dms overhead=%dms requests=%d prompt=%dB tools=%dB recovery=%s\n",
			cell.Task, cell.Runner, resultLabel(cell),
			cell.WallMillis, cell.ModelMillis, cell.OverheadMillis,
			cell.Requests, cell.PromptBytes, cell.ToolSchemaBytes, cell.Recovery)
	}
	for _, failure := range cell.Failures {
		fmt.Fprintf(out, "  - %s\n", failure)
	}
	if cell.Status == "failed" && strings.TrimSpace(cell.Stderr) != "" {
		for _, line := range stderrLines(cell.Stderr, 10) {
			fmt.Fprintf(out, "  stderr: %s\n", line)
		}
	}
}

// stderrLines keeps at most limit lines of a runner's diagnostics. The head is
// what names the cause (the panic line, the traceback's exception).
func stderrLines(text string, limit int) []string {
	var lines []string
	for _, line := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		lines = append(lines, line)
		if len(lines) == limit {
			lines = append(lines, "...")
			break
		}
	}
	return lines
}

// orderedCells lists the cells in report order: task by task, in the order the
// report names the tasks, and runner by runner within each task. A reader
// comparing frameworks reads down one task's rows.
func orderedCells(report *Report) []Cell {
	var ordered []Cell
	for _, task := range report.Tasks {
		for _, runner := range report.Runners {
			for _, cell := range report.Cells {
				if cell.Task == task && cell.Runner == runner {
					ordered = append(ordered, cell)
				}
			}
		}
	}
	return ordered
}

// RenderText renders the final table.
func RenderText(report *Report) string {
	var builder strings.Builder
	builder.WriteString("\n")
	writer := tabwriter.NewWriter(&builder, 0, 4, 2, ' ', 0)
	fmt.Fprintln(writer, "TASK\tRUNNER\tRESULT\tN\tWALL_MS\tOVERHEAD_MS\tREQUESTS\tPROMPT_B\tTOOLS_B\tRECOVERY")
	for _, cell := range orderedCells(report) {
		fmt.Fprintf(writer, "%s\t%s\t%s\t%d\t%d\t%d\t%d\t%d\t%d\t%s\n",
			cell.Task, cell.Runner, resultLabel(cell), cell.Samples,
			cell.WallMillis, cell.OverheadMillis,
			cell.Requests, cell.PromptBytes, cell.ToolSchemaBytes, cell.Recovery)
	}
	_ = writer.Flush()
	if len(report.Unavailable) > 0 {
		builder.WriteString("\nunavailable runners:\n")
		for _, unavailable := range report.Unavailable {
			fmt.Fprintf(&builder, "  %s: %s\n    install with: %s\n",
				unavailable.Runner, firstLine(unavailable.Detail), unavailable.Install)
		}
	}
	builder.WriteString("\nmetrics: N is how many times each cell was run in a fresh workspace; WALL_MS and\n")
	builder.WriteString("OVERHEAD_MS are medians over those N runs, and overhead is wall minus the scripted\n")
	builder.WriteString("endpoint's own service time; REQUESTS, PROMPT_B and TOOLS_B come from the frozen\n")
	builder.WriteString("script and are identical across repeats -- a cell where they differ is a failure, not\n")
	builder.WriteString("an average; recovery is the durable task's phase sequence in one cell.\n")
	return builder.String()
}

// RenderMarkdown renders the same report as a Markdown table, for the ADR, a
// CI summary, or a pull request comment.
func RenderMarkdown(report *Report) string {
	var builder strings.Builder
	builder.WriteString("# Cross-framework benchmark\n\n")
	builder.WriteString("| Task | Runner | Result | N | Wall (ms, median) | Overhead (ms, median) | Requests | Prompt bytes | Tool-schema bytes | Recovery |\n")
	builder.WriteString("| --- | --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | --- |\n")
	for _, cell := range orderedCells(report) {
		fmt.Fprintf(&builder, "| %s | %s | %s | %d | %d | %d | %d | %d | %d | %s |\n",
			cell.Task, cell.Runner, resultLabel(cell), cell.Samples,
			cell.WallMillis, cell.OverheadMillis,
			cell.Requests, cell.PromptBytes, cell.ToolSchemaBytes, cell.Recovery)
	}
	if len(report.Unavailable) > 0 {
		builder.WriteString("\n## Unavailable runners\n\n")
		for _, unavailable := range report.Unavailable {
			fmt.Fprintf(&builder, "- **%s**: %s — install with `%s`\n",
				unavailable.Runner, firstLine(unavailable.Detail), unavailable.Install)
		}
	}
	builder.WriteString("\n`N` is how many times each cell was run in a fresh workspace; wall and overhead are\n")
	builder.WriteString("medians over those runs, where overhead is wall-clock minus the scripted endpoint's\n")
	builder.WriteString("own service time. `prompt bytes` counts the request bodies the endpoint received;\n")
	builder.WriteString("`tool-schema bytes` is the share of them the tools array occupied. Requests and bytes\n")
	builder.WriteString("come from the frozen script, so they are identical across repeats: a cell where they\n")
	builder.WriteString("differ is reported as a failure rather than averaged. An `unsupported` cell means the\n")
	builder.WriteString("framework cannot do what the task requires; it is neither a success nor a failure. The\n")
	builder.WriteString("`-json` report carries every repeat's own sample.\n")
	return builder.String()
}
