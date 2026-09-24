// Package harness orchestrates the benchmark: it selects tasks and runners,
// gives each (runner, task) pair a fresh workspace and a fresh scripted
// endpoint, runs the phases the task requires, verifies the result, aggregates
// the four metrics, and renders the report.
//
// It lives in a package rather than in package main so the same code path the
// command line uses is the one the end-to-end test exercises. A harness that
// can only be driven from a process boundary is a harness whose failure modes
// are only discovered in CI.
package harness

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/feiyu912/zenforge/benchmarks/internal/runner"
	"github.com/feiyu912/zenforge/benchmarks/internal/scripted"
	"github.com/feiyu912/zenforge/benchmarks/tasks"
)

// modelID is the model every framework is told to request. It is identical
// across runners so the model cannot be a variable in the comparison.
const modelID = "scripted-model"

// scriptedAPIKey is a placeholder credential. The endpoint ignores it; it exists
// because a real OpenAI-compatible client refuses to start without one.
const scriptedAPIKey = "benchmark-scripted-key"

// maxRepeat bounds how many times one cell may be repeated. Each repeat is a
// whole task run per runner in its own directories, so the ceiling exists to
// keep an accidental `-repeat 1000` from turning a comparison into an overnight
// job.
const maxRepeat = 64

// Options configures one harness run.
type Options struct {
	// TaskSpec is "all" or a comma list of task ids.
	TaskSpec string
	// RunnerSpec is "all" or a comma list of runner ids.
	RunnerSpec string
	// LiveModel, when set, asks for a real provider instead of the scripted
	// endpoint. It is not implemented in this chain; see Run.
	LiveModel string
	// Repeat is how many times each (task, runner) cell is run, in fresh
	// workspaces and state directories. Zero means one. The wall-clock column
	// is the median over the repeats, and the cost columns must be identical
	// across them or the cell fails.
	Repeat int
	// ModuleRoot is the benchmarks module directory. Empty detects it by
	// walking up from the working directory.
	ModuleRoot string
	// BaseDir is where the per-run temporary tree is created. Empty uses the
	// system temporary directory.
	BaseDir string
	// Out receives one line per run as it finishes. Empty discards them.
	Out io.Writer
}

// PhaseReport is one runner process in one pass.
type PhaseReport struct {
	Phase      string `json:"phase"`
	Status     string `json:"status"`
	Detail     string `json:"detail"`
	ExitCode   int    `json:"exitCode"`
	WallMillis int64  `json:"wallMillis"`
	Stderr     string `json:"stderr,omitempty"`
}

// PassReport is one approval pass of a task. Most tasks have exactly one;
// approve-command has two, because the frozen contract makes both the approved
// and the rejected outcome part of success.
type PassReport struct {
	// Repeat is the 0-based repeat this pass belongs to. A task's whole run --
	// every approval pass of it -- is repeated, so passes with the same Repeat
	// form one complete sample of the cell.
	Repeat          int           `json:"repeat"`
	Approval        string        `json:"approval"`
	Status          string        `json:"status"`
	Success         bool          `json:"success"`
	Phases          []PhaseReport `json:"phases"`
	Checks          []string      `json:"checks,omitempty"`
	Failures        []string      `json:"failures,omitempty"`
	Requests        int           `json:"requests"`
	PromptBytes     int64         `json:"promptBytes"`
	ToolSchemaBytes int64         `json:"toolSchemaBytes"`
	WallMillis      int64         `json:"wallMillis"`
	ModelMillis     int64         `json:"modelMillis"`
}

// RepeatSample is one repeat of a cell, summed over its approval passes. These
// are the samples the medians are taken from and the ones the JSON report
// publishes, because a median without its samples is not a measurement.
type RepeatSample struct {
	Repeat          int    `json:"repeat"`
	Status          string `json:"status"`
	Success         bool   `json:"success"`
	WallMillis      int64  `json:"wallMillis"`
	ModelMillis     int64  `json:"modelMillis"`
	OverheadMillis  int64  `json:"overheadMillis"`
	Requests        int    `json:"requests"`
	PromptBytes     int64  `json:"promptBytes"`
	ToolSchemaBytes int64  `json:"toolSchemaBytes"`
}

// Cell is one (task, runner) result: the unit the report table shows. Its
// wall-clock figures are medians over Samples repeats, and its cost figures are
// the value every repeat agreed on: requests and bytes come from a frozen
// script, so a repeat that changes them is a nondeterminism finding rather than
// noise to average away.
type Cell struct {
	Task            string         `json:"task"`
	Runner          string         `json:"runner"`
	Status          string         `json:"status"`
	Success         bool           `json:"success"`
	Unsupported     bool           `json:"unsupported"`
	Samples         int            `json:"samples"`
	WallMillis      int64          `json:"wallMillis"`
	MinWallMillis   int64          `json:"minWallMillis"`
	MaxWallMillis   int64          `json:"maxWallMillis"`
	ModelMillis     int64          `json:"modelMillis"`
	OverheadMillis  int64          `json:"overheadMillis"`
	OverheadMin     int64          `json:"overheadMinMillis"`
	OverheadMax     int64          `json:"overheadMaxMillis"`
	Requests        int            `json:"requests"`
	PromptBytes     int64          `json:"promptBytes"`
	ToolSchemaBytes int64          `json:"toolSchemaBytes"`
	Recovery        string         `json:"recovery"`
	Detail          string         `json:"detail"`
	Failures        []string       `json:"failures,omitempty"`
	Stderr          string         `json:"stderr,omitempty"`
	Repeats         []RepeatSample `json:"repeats"`
	Passes          []PassReport   `json:"passes"`
}

// Unavailable names a runner the harness could not start, with the command
// that installs what it is missing.
type Unavailable struct {
	Runner  string `json:"runner"`
	Display string `json:"display"`
	Detail  string `json:"detail"`
	Install string `json:"install"`
}

// Report is one harness run, ready to be written as JSON or rendered as a
// table.
type Report struct {
	Tasks       []string      `json:"tasks"`
	Runners     []string      `json:"runners"`
	Cells       []Cell        `json:"cells"`
	Unavailable []Unavailable `json:"unavailable,omitempty"`
}

// Run executes the requested tasks for the requested runners.
//
// It returns an error only for a request it cannot honor at all (an unknown id,
// live mode, no module root). A runner being unavailable or a task failing is a
// *result*: it is recorded in the report so it stays visible, and ExitError
// turns it into the process's non-zero exit at the very end.
func Run(ctx context.Context, opts Options) (*Report, error) {
	if strings.TrimSpace(opts.LiveModel) != "" {
		return nil, fmt.Errorf("-live-model %q is not implemented in this chain: live mode "+
			"replaces the scripted endpoint with a real provider and therefore needs a credential "+
			"the benchmark's CI does not have (see benchmarks/README.md, Live mode)",
			strings.TrimSpace(opts.LiveModel))
	}
	repeat := opts.Repeat
	switch {
	case repeat == 0:
		repeat = 1
	case repeat < 0:
		return nil, fmt.Errorf("-repeat must be at least 1, got %d", repeat)
	case repeat > maxRepeat:
		return nil, fmt.Errorf("-repeat %d is above the %d-repeat ceiling: each repeat is a whole task run "+
			"per runner, and the benchmark's CI budget is not unbounded", repeat, maxRepeat)
	}
	selectedTasks, err := tasks.Select(opts.TaskSpec)
	if err != nil {
		return nil, err
	}
	selectedRunners, err := selectRunners(opts.RunnerSpec)
	if err != nil {
		return nil, err
	}
	moduleRoot := opts.ModuleRoot
	if strings.TrimSpace(moduleRoot) == "" {
		moduleRoot, err = detectModuleRoot()
		if err != nil {
			return nil, err
		}
	}
	out := opts.Out
	if out == nil {
		out = io.Discard
	}
	tempRoot, err := os.MkdirTemp(opts.BaseDir, "zenforge-bench-")
	if err != nil {
		return nil, fmt.Errorf("create benchmark workspace: %w", err)
	}
	defer os.RemoveAll(tempRoot)

	goBinary, _ := exec.LookPath("go")
	env := &buildEnvironment{moduleRoot: moduleRoot, tempDir: tempRoot, goBinary: goBinary}

	report := &Report{Tasks: taskIDs(selectedTasks), Runners: runnerIDsFor(selectedRunners)}
	for _, registration := range selectedRunners {
		prepared, prepareErr := registration.prepare(ctx, env)
		if prepareErr != nil {
			report.Unavailable = append(report.Unavailable, Unavailable{
				Runner:  registration.id,
				Display: registration.display,
				Detail:  prepareErr.Error(),
				Install: registration.install,
			})
			fmt.Fprintf(out, "runner %s: unavailable: %v\n  install with: %s\n",
				registration.id, prepareErr, registration.install)
			for _, task := range selectedTasks {
				report.Cells = append(report.Cells, unavailableCell(task, registration, prepareErr, repeat))
			}
			continue
		}
		for _, task := range selectedTasks {
			cell := runCell(ctx, task, registration, prepared, tempRoot, repeat)
			report.Cells = append(report.Cells, cell)
			printCell(out, cell)
		}
	}
	return report, nil
}

// ExitError summarizes every reason the harness run must not exit zero: a
// runner that could not be started, or a task that failed. An `unsupported`
// cell is deliberately not one of them: a framework that cannot do a task is a
// result of the comparison, not a broken benchmark.
func (r *Report) ExitError() error {
	var problems []string
	for _, unavailable := range r.Unavailable {
		problems = append(problems, fmt.Sprintf("runner %s is unavailable: %s (install with: %s)",
			unavailable.Runner, firstLine(unavailable.Detail), unavailable.Install))
	}
	for _, cell := range r.Cells {
		if cell.Status != string(runner.StatusFailed) {
			continue
		}
		detail := strings.TrimSpace(cell.Detail)
		if detail == "" && len(cell.Failures) > 0 {
			detail = cell.Failures[0]
		}
		if detail != "" {
			detail = ": " + firstLine(detail)
		}
		problems = append(problems, fmt.Sprintf("%s/%s failed%s", cell.Task, cell.Runner, detail))
	}
	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("benchmark failed:\n  - %s", strings.Join(problems, "\n  - "))
}

// runCell runs every approval pass the task requires, repeat times, and
// aggregates them into one report row.
//
// Latency is reported as a median over the repeats: one sample of a wall clock
// on a machine that is simultaneously compiling and running other frameworks is
// an anecdote, not a measurement. The cost figures are not averaged, because
// they are not noisy: requests and bytes come from a frozen script, so repeats
// that disagree are reported as a failure instead of being smoothed away.
func runCell(ctx context.Context, task tasks.Metadata, registration runnerRegistration, prepared preparedRunner, tempRoot string, repeat int) Cell {
	cell := Cell{Task: task.ID, Runner: registration.id, Passes: []PassReport{}, Failures: []string{}, Samples: repeat}
	for index := range repeat {
		for _, approval := range task.Approvals {
			pass := runPass(ctx, task, approval, registration, prepared, tempRoot, index)
			cell.Passes = append(cell.Passes, pass)
		}
	}

	cell.Repeats = repeatSamples(cell.Passes, repeat)
	for _, sample := range cell.Repeats {
		cell.Failures = append(cell.Failures, qualifyRepeat(sample.Repeat, repeat, repeatFailures(cell.Passes, sample.Repeat))...)
	}
	cell.ModelMillis = medianInt64(sampleField(cell.Repeats, func(s RepeatSample) int64 { return s.ModelMillis }))
	cell.WallMillis = medianInt64(sampleField(cell.Repeats, func(s RepeatSample) int64 { return s.WallMillis }))
	cell.MinWallMillis, cell.MaxWallMillis = extremes(sampleField(cell.Repeats, func(s RepeatSample) int64 { return s.WallMillis }))
	cell.OverheadMillis = medianInt64(sampleField(cell.Repeats, func(s RepeatSample) int64 { return s.OverheadMillis }))
	cell.OverheadMin, cell.OverheadMax = extremes(sampleField(cell.Repeats, func(s RepeatSample) int64 { return s.OverheadMillis }))
	cell.Requests = medianInt(sampleField(cell.Repeats, func(s RepeatSample) int64 { return int64(s.Requests) }))
	cell.PromptBytes = medianInt64(sampleField(cell.Repeats, func(s RepeatSample) int64 { return s.PromptBytes }))
	cell.ToolSchemaBytes = medianInt64(sampleField(cell.Repeats, func(s RepeatSample) int64 { return s.ToolSchemaBytes }))

	for _, pass := range cell.Passes {
		cell.Stderr += passStderr(pass)
		for _, phase := range pass.Phases {
			if detail := strings.TrimSpace(phase.Detail); detail != "" {
				cell.Detail = detail
			}
		}
	}
	cell.Detail = strings.TrimRight(cell.Detail, "\n")
	cell.Stderr = strings.TrimRight(cell.Stderr, "\n")

	cell.Status = cellStatus(cell.Passes)
	if failure := checkDeterminism(task, cell.Repeats); failure != "" {
		// A nondeterministic cost metric invalidates the comparison, so it is a
		// failure of the cell whatever else happened in it.
		cell.Failures = append(cell.Failures, failure)
		cell.Status = string(runner.StatusFailed)
	}
	cell.Success = cell.Status == string(runner.StatusCompleted)
	cell.Unsupported = cell.Status == string(runner.StatusUnsupported)
	cell.Recovery = recoveryText(task, cell.Passes)
	return cell
}

// repeatSamples sums each repeat's approval passes into one sample.
func repeatSamples(passes []PassReport, repeat int) []RepeatSample {
	samples := make([]RepeatSample, 0, repeat)
	for index := range repeat {
		sample := RepeatSample{Repeat: index, Status: string(runner.StatusCompleted), Success: true}
		repeatPasses := []PassReport{}
		for _, pass := range passes {
			if pass.Repeat != index {
				continue
			}
			sample.WallMillis += pass.WallMillis
			sample.ModelMillis += pass.ModelMillis
			sample.Requests += pass.Requests
			sample.PromptBytes += pass.PromptBytes
			sample.ToolSchemaBytes += pass.ToolSchemaBytes
			repeatPasses = append(repeatPasses, pass)
			sample.Success = sample.Success && pass.Success
		}
		sample.OverheadMillis = sample.WallMillis - sample.ModelMillis
		if sample.OverheadMillis < 0 {
			// The endpoint's clock and the process's clock measure overlapping
			// intervals; a tiny scripted reply can round the difference below
			// zero. Zero is the honest floor.
			sample.OverheadMillis = 0
		}
		sample.Status = cellStatus(repeatPasses)
		samples = append(samples, sample)
	}
	return samples
}

// checkDeterminism reports the first repeat whose request count or byte counts
// differ from the first repeat's. The script is fixed, so any difference means
// the endpoint or the runner behaved nondeterministically: publishing an
// average of the two would hide a bug behind a number.
func checkDeterminism(task tasks.Metadata, samples []RepeatSample) string {
	if len(samples) < 2 {
		return ""
	}
	first := samples[0]
	for _, sample := range samples[1:] {
		switch {
		case sample.Requests != first.Requests:
			return fmt.Sprintf("repeat %d of %s made %d model request(s); repeat 1 made %d. The script is fixed, so the same task must make the same number of requests every time",
				sample.Repeat+1, task.ID, sample.Requests, first.Requests)
		case sample.PromptBytes != first.PromptBytes:
			return fmt.Sprintf("repeat %d of %s sent %d prompt bytes; repeat 1 sent %d. Prompt cost must not vary between repeats of the same task",
				sample.Repeat+1, task.ID, sample.PromptBytes, first.PromptBytes)
		case sample.ToolSchemaBytes != first.ToolSchemaBytes:
			return fmt.Sprintf("repeat %d of %s advertised %d tool-schema bytes; repeat 1 advertised %d. Tool schemas must not vary between repeats of the same task",
				sample.Repeat+1, task.ID, sample.ToolSchemaBytes, first.ToolSchemaBytes)
		}
	}
	return ""
}

// repeatFailures collects the verifier and phase failures of one repeat.
func repeatFailures(passes []PassReport, repeat int) []string {
	var failures []string
	for _, pass := range passes {
		if pass.Repeat == repeat {
			failures = append(failures, qualify(pass.Approval, pass.Failures)...)
		}
	}
	return failures
}

// qualifyRepeat tags a failure with its repeat, so a reader can tell a flake
// from a rule.
func qualifyRepeat(repeat, repeats int, failures []string) []string {
	if repeats <= 1 || len(failures) == 0 {
		return failures
	}
	tagged := make([]string, 0, len(failures))
	for _, failure := range failures {
		tagged = append(tagged, fmt.Sprintf("[repeat %d] %s", repeat+1, failure))
	}
	return tagged
}

// passStderr keeps every phase's diagnostics, so a repeat cannot hide an
// earlier one's errors.
func passStderr(pass PassReport) string {
	var builder strings.Builder
	for _, phase := range pass.Phases {
		if phase.Stderr == "" {
			continue
		}
		builder.WriteString(strings.TrimRight(phase.Stderr, "\n"))
		builder.WriteString("\n")
	}
	return builder.String()
}

// sampleField projects one numeric field out of every sample.
func sampleField(samples []RepeatSample, field func(RepeatSample) int64) []int64 {
	values := make([]int64, 0, len(samples))
	for _, sample := range samples {
		values = append(values, field(sample))
	}
	return values
}

// medianInt64 returns the middle value of the samples, or the mean of the two
// middle values when there is an even number of them. The input is copied, so
// the caller's sample order is untouched.
func medianInt64(values []int64) int64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]int64(nil), values...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	middle := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return sorted[middle]
	}
	return (sorted[middle-1] + sorted[middle]) / 2
}

// medianInt is medianInt64 for whole counts.
func medianInt(values []int64) int { return int(medianInt64(values)) }

// extremes returns the smallest and largest sample; an empty set is 0, 0.
func extremes(values []int64) (int64, int64) {
	if len(values) == 0 {
		return 0, 0
	}
	low, high := values[0], values[0]
	for _, value := range values[1:] {
		if value < low {
			low = value
		}
		if value > high {
			high = value
		}
	}
	return low, high
}

// runPass runs one approval pass of a task: a fresh workspace, a fresh state
// directory, a fresh scripted endpoint, the phases the task requires, and the
// verifier. Every repeat gets its own directories, so one repeat can never
// inherit a file another repeat wrote.
func runPass(ctx context.Context, task tasks.Metadata, approval string, registration runnerRegistration, prepared preparedRunner, tempRoot string, repeat int) PassReport {
	pass := PassReport{Repeat: repeat, Approval: approval, Phases: []PhaseReport{}}
	dir := filepath.Join(tempRoot, registration.id, task.ID, fmt.Sprintf("repeat-%d", repeat+1), approval)
	workspace := filepath.Join(dir, "workspace")
	stateDir := filepath.Join(dir, "state")
	results := filepath.Join(dir, "results")
	for _, path := range []string{workspace, stateDir, results} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			pass.Status = string(runner.StatusFailed)
			pass.Failures = append(pass.Failures, fmt.Sprintf("create %s: %v", path, err))
			return pass
		}
	}
	if err := task.SeedWorkspace(workspace); err != nil {
		pass.Status = string(runner.StatusFailed)
		pass.Failures = append(pass.Failures, fmt.Sprintf("seed workspace: %v", err))
		return pass
	}
	endpoint, err := scripted.New(task.Script.Turns)
	if err != nil {
		pass.Status = string(runner.StatusFailed)
		pass.Failures = append(pass.Failures, err.Error())
		return pass
	}
	defer endpoint.Close()

	base := runner.Spec{
		RunnerID:  registration.id,
		Task:      task.ID,
		Command:   prepared.command,
		Dir:       prepared.dir,
		BaseURL:   endpoint.BaseURL(),
		APIKey:    scriptedAPIKey,
		Model:     modelID,
		Query:     task.Query,
		Workspace: workspace,
		StateDir:  stateDir,
		Approval:  approval,
		Install:   registration.install,
		ExtraEnv:  prepared.extraEnv,
	}

	runSpec := base
	runSpec.Phase = tasks.PhaseRun
	runSpec.RequirePause = task.Resumable()
	runSpec.ResultPath = filepath.Join(results, "run.json")
	runOutcome := runner.Run(ctx, runSpec)
	pass.Phases = append(pass.Phases, phaseReport(runOutcome))

	stateFilesAfterRun := countFiles(stateDir)
	if task.Resumable() && runOutcome.Status == runner.StatusPaused {
		resumeSpec := base
		resumeSpec.Phase = tasks.PhaseResume
		resumeSpec.ResultPath = filepath.Join(results, "resume.json")
		pass.Phases = append(pass.Phases, phaseReport(runner.Run(ctx, resumeSpec)))
	}

	for _, phase := range pass.Phases {
		pass.WallMillis += phase.WallMillis
	}
	pass.ModelMillis = endpoint.ModelServiceTime().Milliseconds()
	pass.Requests = endpoint.Count()
	pass.PromptBytes = endpoint.PromptBytes()
	pass.ToolSchemaBytes = endpoint.ToolSchemaBytes()

	// A framework that reported the task unsupported, or could not be started,
	// is not judged against the artifact: there is nothing to judge, and a list
	// of missing files would read as a failure the cell does not claim to be.
	processStatus := processStatus(pass.Phases)
	if processStatus == runner.StatusUnsupported || processStatus == runner.StatusUnavailable {
		pass.Status = string(processStatus)
		pass.Success = false
		return pass
	}

	phaseInputs := make([]tasks.PhaseResult, 0, len(pass.Phases))
	for _, phase := range pass.Phases {
		phaseInputs = append(phaseInputs, tasks.PhaseResult{
			Phase:  phase.Phase,
			Status: phase.Status,
			Detail: phase.Detail,
		})
	}
	verdict := tasks.Verify(tasks.Input{
		Metadata:           task,
		Approval:           approval,
		Workspace:          workspace,
		StateDir:           stateDir,
		StateFilesAfterRun: stateFilesAfterRun,
		Phases:             phaseInputs,
		Requests:           endpoint.Requests(),
	})
	pass.Checks = verdict.Checks
	pass.Failures = append(pass.Failures, verdict.Failures...)
	pass.Status = string(passStatus(pass.Phases, verdict.Success))
	pass.Success = pass.Status == string(runner.StatusCompleted)
	return pass
}

// processStatus is the worst status the phases themselves reported, before any
// verification. It is what decides whether verification is meaningful at all.
func processStatus(phases []PhaseReport) runner.Status {
	status := runner.StatusCompleted
	for _, phase := range phases {
		if statusRank(runner.Status(phase.Status)) > statusRank(status) {
			status = runner.Status(phase.Status)
		}
	}
	return status
}

// statusRank orders the statuses by how much they override one another:
// unavailable outranks failed, failed outranks unsupported, and completed is
// the floor.
func statusRank(status runner.Status) int {
	switch status {
	case runner.StatusUnavailable:
		return 3
	case runner.StatusFailed:
		return 2
	case runner.StatusUnsupported:
		return 1
	default:
		return 0
	}
}

// phaseReport converts one runner outcome into the report's shape.
func phaseReport(outcome runner.Outcome) PhaseReport {
	return PhaseReport{
		Phase:      outcome.Phase,
		Status:     string(outcome.Status),
		Detail:     strings.TrimSpace(outcome.Detail),
		ExitCode:   outcome.ExitCode,
		WallMillis: outcome.WallClock.Milliseconds(),
		Stderr:     outcome.Stderr,
	}
}

// passStatus combines the phase statuses with the verifier's judgment.
// Precedence is unavailable, failed, unsupported, completed: a runner that
// cannot start outranks everything, and a verification failure is a failure
// even when every process exited zero.
func passStatus(phases []PhaseReport, verified bool) runner.Status {
	statuses := make([]runner.Status, 0, len(phases))
	for _, phase := range phases {
		statuses = append(statuses, runner.Status(phase.Status))
	}
	for _, want := range []runner.Status{runner.StatusUnavailable, runner.StatusFailed, runner.StatusUnsupported} {
		for _, status := range statuses {
			if status == want {
				return want
			}
		}
	}
	if !verified {
		return runner.StatusFailed
	}
	return runner.StatusCompleted
}

// cellStatus combines every pass of a cell.
func cellStatus(passes []PassReport) string {
	status := runner.StatusCompleted
	for _, pass := range passes {
		if statusRank(runner.Status(pass.Status)) > statusRank(status) {
			status = runner.Status(pass.Status)
		}
	}
	return string(status)
}

// recoveryText describes what the durable task's two processes did, and "n/a"
// for a task that has no recovery phase.
func recoveryText(task tasks.Metadata, passes []PassReport) string {
	if !task.Resumable() {
		return "n/a"
	}
	for _, pass := range passes {
		if len(pass.Phases) == 0 {
			return "not run"
		}
		parts := make([]string, 0, len(pass.Phases))
		for _, phase := range pass.Phases {
			parts = append(parts, phase.Status)
		}
		return strings.Join(parts, " -> ")
	}
	return "not run"
}

// unavailableCell is the row a runner gets when it could not be started. It
// stays in the table so a missing dependency is visible rather than a column
// that quietly disappeared.
func unavailableCell(task tasks.Metadata, registration runnerRegistration, cause error, repeat int) Cell {
	return Cell{
		Task:     task.ID,
		Runner:   registration.id,
		Status:   string(runner.StatusUnavailable),
		Samples:  repeat,
		Recovery: recoveryText(task, nil),
		Detail:   firstLine(cause.Error()),
		Repeats:  []RepeatSample{},
	}
}

// countFiles counts the regular files under a directory, which is how "the
// first process left durable state behind" is observed.
func countFiles(root string) int {
	count := 0
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !entry.IsDir() {
			count++
		}
		return nil
	})
	return count
}

// detectModuleRoot finds the benchmarks module by walking up from the working
// directory. A caller may override it, which is what makes the harness usable
// from a test in another package directory.
func detectModuleRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		data, readErr := os.ReadFile(filepath.Join(dir, "go.mod"))
		if readErr == nil && strings.Contains(string(data), "module github.com/feiyu912/zenforge/benchmarks") {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("benchmarks module root not found: run the harness from within benchmarks/")
		}
		dir = parent
	}
}

func taskIDs(selected []tasks.Metadata) []string {
	ids := make([]string, 0, len(selected))
	for _, task := range selected {
		ids = append(ids, task.ID)
	}
	return ids
}

func runnerIDsFor(selected []runnerRegistration) []string {
	ids := make([]string, 0, len(selected))
	for _, registration := range selected {
		ids = append(ids, registration.id)
	}
	return ids
}

// qualify tags a pass's failures with the pass they came from: the same task
// can run under more than one approval mode, and a reader has to be able to
// tell which one an artifact failure belongs to.
func qualify(approval string, failures []string) []string {
	if len(failures) == 0 {
		return nil
	}
	out := make([]string, 0, len(failures))
	for _, failure := range failures {
		out = append(out, fmt.Sprintf("[%s] %s", approval, failure))
	}
	return out
}

// firstLine keeps an error summary to one line for a table cell.
func firstLine(text string) string {
	text = strings.TrimSpace(text)
	if index := strings.IndexByte(text, '\n'); index >= 0 {
		text = text[:index] + " ..."
	}
	return text
}
