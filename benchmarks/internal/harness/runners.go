package harness

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// runnerRegistration describes one framework runner: how to start it, and how
// to install it when it cannot be started.
//
// The harness treats every entry identically. There is no privileged path for
// the ZenForge runner: it is a subprocess that reads the documented environment
// and writes the documented result file, exactly like the Python and Eino
// runners.
type runnerRegistration struct {
	// id is the name the -runners flag uses.
	id string
	// display is the column name in the report.
	display string
	// install is the exact command that makes this runner available. It is
	// printed when the runner is unavailable, so that a missing dependency is a
	// one-line fix rather than a silently dropped column.
	install string
	// prepare locates or builds the runner, or explains why it is unavailable.
	prepare func(ctx context.Context, env *buildEnvironment) (preparedRunner, error)
}

// preparedRunner is a runnable command.
type preparedRunner struct {
	command  []string
	dir      string
	extraEnv []string
}

// buildEnvironment is what preparing a runner may need from the host.
type buildEnvironment struct {
	moduleRoot string
	tempDir    string
	goBinary   string
}

// registrations is the frozen runner roster. Order is report order.
var registrations = []runnerRegistration{
	{
		id:      "zenforge",
		display: "zenforge",
		install: "go is required; the runner is built from this module and needs no other dependency",
		prepare: prepareZenforge,
	},
	{
		id:      "deepagents",
		display: "deepagents",
		install: "python3 -m venv .venv && .venv/bin/pip install -r runners/python/requirements.txt",
		prepare: func(ctx context.Context, env *buildEnvironment) (preparedRunner, error) {
			return preparePython(ctx, env, "deepagents", "deepagents")
		},
	},
	{
		id:      "langgraph",
		display: "langgraph",
		install: "python3 -m venv .venv && .venv/bin/pip install -r runners/python/requirements.txt",
		prepare: func(ctx context.Context, env *buildEnvironment) (preparedRunner, error) {
			return preparePython(ctx, env, "langgraph", "langgraph")
		},
	},
	{
		id:      "eino",
		display: "eino",
		install: "(cd runners/eino && go mod download)",
		prepare: prepareEino,
	},
}

// runnerIDs lists every registered runner id.
func runnerIDs() []string {
	ids := make([]string, 0, len(registrations))
	for _, registration := range registrations {
		ids = append(ids, registration.id)
	}
	return ids
}

// selectRunners resolves "-runners all" or a comma list. An unknown id is an
// error, because a typo must not silently reduce the comparison.
func selectRunners(spec string) ([]runnerRegistration, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" || spec == "all" {
		return append([]runnerRegistration(nil), registrations...), nil
	}
	var selected []runnerRegistration
	seen := map[string]bool{}
	for _, raw := range strings.Split(spec, ",") {
		id := strings.TrimSpace(raw)
		if id == "" {
			continue
		}
		registration, ok := lookupRunner(id)
		if !ok {
			return nil, fmt.Errorf("unknown runner %q: want one of %s", id, strings.Join(runnerIDs(), ", "))
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		selected = append(selected, registration)
	}
	if len(selected) == 0 {
		return nil, fmt.Errorf("no runners selected from %q", spec)
	}
	return selected, nil
}

func lookupRunner(id string) (runnerRegistration, bool) {
	for _, registration := range registrations {
		if registration.id == id {
			return registration, true
		}
	}
	return runnerRegistration{}, false
}

// prepareZenforge builds the reference runner from this module. Building once,
// before any measurement, keeps compilation out of the wall-clock the report
// attributes to the framework.
func prepareZenforge(ctx context.Context, env *buildEnvironment) (preparedRunner, error) {
	if env.goBinary == "" {
		return preparedRunner{}, fmt.Errorf("go is not on PATH, so the runner cannot be built")
	}
	binary := filepath.Join(env.tempDir, "zenforge-runner")
	command := exec.CommandContext(ctx, env.goBinary, "build", "-o", binary, "./cmd/zenforge-runner")
	command.Dir = env.moduleRoot
	command.Env = os.Environ()
	if output, err := command.CombinedOutput(); err != nil {
		return preparedRunner{}, fmt.Errorf("build ./cmd/zenforge-runner: %v\n%s", err, strings.TrimSpace(string(output)))
	}
	return preparedRunner{command: []string{binary}, dir: env.moduleRoot}, nil
}

// preparePython locates an interpreter that can import the framework's Python
// package and the runner script that uses it.
//
// It prefers a virtual environment in the benchmarks directory because that is
// what the README's install command creates; falling back to whatever python3
// is on PATH means a machine that installed the packages globally still works.
func preparePython(ctx context.Context, env *buildEnvironment, id, module string) (preparedRunner, error) {
	script, err := findPythonScript(env.moduleRoot, id)
	if err != nil {
		return preparedRunner{}, err
	}
	interpreter := ""
	for _, candidate := range []string{
		filepath.Join(env.moduleRoot, ".venv", "bin", "python3"),
		filepath.Join(env.moduleRoot, ".venv", "bin", "python"),
	} {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			interpreter = candidate
			break
		}
	}
	if interpreter == "" {
		for _, name := range []string{"python3", "python"} {
			if path, err := exec.LookPath(name); err == nil {
				interpreter = path
				break
			}
		}
	}
	if interpreter == "" {
		return preparedRunner{}, fmt.Errorf("no python3 interpreter found on PATH")
	}
	// Importing the framework is the honest availability test: an interpreter
	// without the package would fail every run for a reason the comparison is
	// not about.
	probe := exec.CommandContext(ctx, interpreter, "-c", "import "+module)
	probe.Env = os.Environ()
	if output, err := probe.CombinedOutput(); err != nil {
		return preparedRunner{}, fmt.Errorf("%s cannot import %s: %v\n%s",
			interpreter, module, err, strings.TrimSpace(string(output)))
	}
	return preparedRunner{command: []string{interpreter, script}, dir: env.moduleRoot}, nil
}

// pythonScriptCandidates are the file layouts the Python runners may use. The
// harness does not own that directory, so it discovers the entry point rather
// than dictating one.
func pythonScriptCandidates(id string) []string {
	return []string{
		filepath.Join("runners", "python", id+"_runner.py"),
		filepath.Join("runners", "python", id+".py"),
		filepath.Join("runners", "python", "run_"+id+".py"),
		filepath.Join("runners", "python", id, "main.py"),
	}
}

func findPythonScript(moduleRoot, id string) (string, error) {
	for _, relative := range pythonScriptCandidates(id) {
		path := filepath.Join(moduleRoot, relative)
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path, nil
		}
	}
	return "", fmt.Errorf("no %s runner script found; looked for %s",
		id, strings.Join(pythonScriptCandidates(id), ", "))
}

// prepareEino builds the Eino runner from its own module. It is a separate
// module so Eino's dependencies never enter the SDK's module graph; the
// harness builds it the same way a user would, from that directory.
func prepareEino(ctx context.Context, env *buildEnvironment) (preparedRunner, error) {
	if env.goBinary == "" {
		return preparedRunner{}, fmt.Errorf("go is not on PATH, so the runner cannot be built")
	}
	dir := filepath.Join(env.moduleRoot, "runners", "eino")
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return preparedRunner{}, fmt.Errorf("runners/eino does not exist")
	}
	if _, err := os.Stat(filepath.Join(dir, "go.mod")); err != nil {
		return preparedRunner{}, fmt.Errorf("runners/eino has no go.mod")
	}
	binary := filepath.Join(env.tempDir, "eino-runner")
	command := exec.CommandContext(ctx, env.goBinary, "build", "-o", binary, ".")
	command.Dir = dir
	command.Env = os.Environ()
	if output, err := command.CombinedOutput(); err != nil {
		return preparedRunner{}, fmt.Errorf("build runners/eino: %v\n%s", err, strings.TrimSpace(string(output)))
	}
	return preparedRunner{command: []string{binary}, dir: dir}, nil
}
