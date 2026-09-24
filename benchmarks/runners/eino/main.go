// Command eino-runner is the Eino runner for the cross-framework benchmark in
// benchmarks/README.md.
//
// It reads the BENCH_* environment, runs one of the three tasks through an Eino
// ReAct agent (ADK ChatModelAgent) against the scripted OpenAI-compatible
// endpoint, and reports through BENCH_RESULT with the documented exit code.
// Diagnostics go to stderr; the runner never prints a report of its own.
package main

import (
	"fmt"
	"os"
)

func main() {
	os.Exit(runMain())
}

func runMain() int {
	cfg, err := LoadConfig(os.Getenv)
	if err != nil {
		fmt.Fprintln(os.Stderr, "eino-runner: "+err.Error())
		// Still report, when the harness gave us somewhere to report to.
		if path := os.Getenv("BENCH_RESULT"); path != "" {
			phase := os.Getenv("BENCH_PHASE")
			if phase == "" {
				phase = PhaseRun
			}
			_ = WriteResult(path, NewResult(os.Getenv("BENCH_TASK"), phase, StatusFailed, err.Error()))
		}
		return ExitFailed
	}

	result := Execute(cfg)

	if err := WriteResult(cfg.ResultPath, result); err != nil {
		fmt.Fprintln(os.Stderr, "eino-runner: writing BENCH_RESULT: "+err.Error())
		return ExitFailed
	}

	fmt.Fprintf(os.Stderr, "eino-runner: task=%s phase=%s status=%s detail=%s\n",
		result.Task, result.Phase, result.Status, result.Detail)

	return result.ExitCode()
}
