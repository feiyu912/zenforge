// Command bench is the cross-framework benchmark harness.
//
// It runs the frozen task set against one or more framework runners and reports
// task success, latency, cost, and recovery. Each runner is a subprocess that
// reads the documented BENCH_* environment and writes a result JSON, so adding
// a framework means adding a command, not adding code to this program.
//
// The default run is hermetic: the ZenForge runner is built from this module and
// talks only to a loopback scripted endpoint, so `go run ./cmd/bench` needs
// nothing but Go. The other runners need their own dependencies, which is why
// the full comparison is opt-in; a runner whose dependencies are missing is
// reported as unavailable with the exact command that installs them, and makes
// the process exit non-zero rather than quietly dropping a column.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/feiyu912/zenforge/benchmarks/internal/harness"
)

func main() {
	var (
		tasksSpec    = flag.String("tasks", "all", "tasks to run: all, or a comma list of edit-file, approve-command, durable-task")
		runnersSpec  = flag.String("runners", "all", "runners to run: all, or a comma list of zenforge, deepagents, langgraph, eino")
		jsonPath     = flag.String("json", "", "write the machine-readable result set to this path")
		markdownPath = flag.String("markdown", "", "write the report table as Markdown to this path")
		liveModel    = flag.String("live-model", "", "run against a real provider as <provider>/<model> (not implemented in this chain)")
		repeat       = flag.Int("repeat", 1, "run each (task, runner) cell this many times in fresh workspaces; wall-clock is reported as the median and the cost metrics must be identical across repeats")
	)
	flag.Parse()

	report, err := harness.Run(context.Background(), harness.Options{
		TaskSpec:   *tasksSpec,
		RunnerSpec: *runnersSpec,
		LiveModel:  *liveModel,
		Repeat:     *repeat,
		Out:        os.Stdout,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "bench:", err)
		os.Exit(2)
	}

	// Artifacts are written before the exit decision: a failing run is exactly
	// the run whose machine-readable record matters most.
	if *jsonPath != "" {
		if err := writeJSON(*jsonPath, report); err != nil {
			fmt.Fprintln(os.Stderr, "bench:", err)
			os.Exit(2)
		}
	}
	if *markdownPath != "" {
		if err := writeFile(*markdownPath, harness.RenderMarkdown(report)); err != nil {
			fmt.Fprintln(os.Stderr, "bench:", err)
			os.Exit(2)
		}
	}
	fmt.Fprint(os.Stdout, harness.RenderText(report))

	if err := report.ExitError(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func writeJSON(path string, report *harness.Report) error {
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(path, string(append(data, '\n')))
}

func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o644)
}
