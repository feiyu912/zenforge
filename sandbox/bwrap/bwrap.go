package bwrap

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/feiyu912/zenforge/sandbox"
)

const (
	defaultShell          = "/bin/sh"
	defaultTimeout        = 30 * time.Second
	defaultMaxOutputBytes = int64(8 << 20)
	internalOutputLimit   = int64(64 << 10)
	argsMetadataKey       = "zenforge.bwrap.args"
	argsHashMetadataKey   = "zenforge.bwrap.argsHash"
	writableRootsMetaKey  = "zenforge.bwrap.writableRoots"
	readableRootsMetaKey  = "zenforge.bwrap.readableRoots"
	readOnlyPathsMetaKey  = "zenforge.bwrap.readOnlyPaths"
	networkMetadataKey    = "zenforge.bwrap.allowNetwork"
)

// Runner executes bubblewrap. It is injectable so tests can exercise the
// lifecycle and error mapping without Linux.
type Runner interface {
	Run(ctx context.Context, executable string, args []string, stdout, stderr io.Writer) error
}

// Config configures the adapter.
type Config struct {
	// Executable is the bubblewrap binary, "bwrap" by default.
	Executable string
	// Shell is the program that receives the command string, "/bin/sh" by
	// default.
	Shell string
	// DefaultWorkingDir seeds a session whose request names none.
	DefaultWorkingDir string
	// FullDiskRead binds the host root read-only instead of starting from an
	// empty tmpfs. It defaults to true, which is the useful setting for a
	// coding agent: everything is readable, only the declared roots are
	// writable.
	FullDiskRead *bool
	// WritableRoots are the roots a session may modify. The working
	// directory is added automatically.
	WritableRoots []string
	// ReadableRoots are extra read roots for a restricted layout.
	ReadableRoots []string
	// ReadOnlyPaths are paths pinned read-only inside writable roots.
	ReadOnlyPaths []string
	// ProtectedNames selects the protected basenames inside writable roots.
	ProtectedNames []string
	// IncludePlatformDefaults adds the platform read roots.
	IncludePlatformDefaults bool
	// AllowNetwork keeps the host network namespace.
	AllowNetwork bool
	// MountProc mounts a fresh /proc (default true).
	MountProc *bool
	// ExtraArgs are host-specific bubblewrap flags.
	ExtraArgs []string
	// DefaultTimeout bounds one execution.
	DefaultTimeout time.Duration
	// MaxOutputBytes caps per-stream output.
	MaxOutputBytes int64
	// Runner replaces real process execution.
	Runner Runner
	// LookPath resolves the executable; nil uses exec.LookPath.
	LookPath func(string) (string, error)
	// SkipPlatformCheck allows exercising the adapter off Linux.
	SkipPlatformCheck bool
}

// Adapter implements sandbox.Sandbox with bubblewrap.
type Adapter struct {
	executable      string
	shell           string
	defaultDir      string
	policy          Policy
	timeout         time.Duration
	maxOutput       int64
	runner          Runner
	lookPath        func(string) (string, error)
	skipPlatform    bool
	fullDiskRead    bool
	includeDefaults bool

	mu       sync.Mutex
	resolved string
	sessions map[string]*session
}

var _ sandbox.Sandbox = (*Adapter)(nil)

type session struct {
	workingDir string
	// prefix is the bubblewrap argument list up to and including the
	// terminating "--"; the sandboxed command is appended per execution.
	prefix []string
	env    map[string]string
}

// New returns an adapter, validating the configured roots.
func New(config Config) (*Adapter, error) {
	executable := strings.TrimSpace(config.Executable)
	if executable == "" {
		executable = defaultExecutable
	}
	shell := strings.TrimSpace(config.Shell)
	if shell == "" {
		shell = defaultShell
	}
	timeout := config.DefaultTimeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	maxOutput := config.MaxOutputBytes
	if maxOutput <= 0 {
		maxOutput = defaultMaxOutputBytes
	}
	fullDiskRead := true
	if config.FullDiskRead != nil {
		fullDiskRead = *config.FullDiskRead
	}
	lookPath := config.LookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	policy := Policy{
		ReadableRoots:           config.ReadableRoots,
		ReadOnlyPaths:           config.ReadOnlyPaths,
		ProtectedNames:          config.ProtectedNames,
		FullDiskRead:            fullDiskRead,
		IncludePlatformDefaults: config.IncludePlatformDefaults,
		AllowNetwork:            config.AllowNetwork,
		MountProc:               config.MountProc,
		ExtraArgs:               config.ExtraArgs,
	}
	return &Adapter{
		executable:   executable,
		shell:        shell,
		defaultDir:   strings.TrimSpace(config.DefaultWorkingDir),
		policy:       policy,
		timeout:      timeout,
		maxOutput:    maxOutput,
		runner:       config.Runner,
		lookPath:     lookPath,
		skipPlatform: config.SkipPlatformCheck,
		sessions:     map[string]*session{},
	}, nil
}

// Open prepares a session: it resolves the working directory, builds the
// bubblewrap argument list, and checks the runner exists.
func (a *Adapter) Open(ctx context.Context, req sandbox.OpenRequest) (*sandbox.Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	key := sandbox.SessionKey(req.RunID, req.SubtaskID)
	if key == "" {
		return nil, fmt.Errorf("%w: run id is required", sandbox.ErrSessionOpenFailed)
	}
	if !a.skipPlatform && a.runner == nil && runtime.GOOS != "linux" {
		return nil, fmt.Errorf("%w: bubblewrap is only available on linux (host is %s)", sandbox.ErrSandboxUnavailable, runtime.GOOS)
	}
	if a.runner == nil {
		resolved, err := a.lookPath(a.executable)
		if err != nil {
			return nil, fmt.Errorf("%w: %s not found: %v", sandbox.ErrSandboxUnavailable, a.executable, err)
		}
		a.mu.Lock()
		a.resolved = resolved
		a.mu.Unlock()
	}

	workingDir, err := a.resolveWorkingDir(req)
	if err != nil {
		return nil, err
	}
	roots := append([]string(nil), a.policy.WritableRoots...)
	roots = append(roots, metadataStringList(req.Metadata, writableRootsMetaKey)...)
	if info, err := os.Stat(workingDir); err == nil && info.IsDir() {
		roots = append(roots, workingDir)
	}
	// Bubblewrap cannot bind a missing target, so drop writable roots that
	// do not exist yet instead of failing the whole session.
	existing := make([]string, 0, len(roots))
	for _, root := range roots {
		if _, err := os.Stat(filepath.Clean(root)); err == nil {
			existing = append(existing, root)
		}
	}
	policy := a.policy
	policy.WritableRoots = existing
	policy.ReadableRoots = append(append([]string(nil), a.policy.ReadableRoots...), metadataStringList(req.Metadata, readableRootsMetaKey)...)
	policy.ReadOnlyPaths = append(append([]string(nil), a.policy.ReadOnlyPaths...), metadataStringList(req.Metadata, readOnlyPathsMetaKey)...)
	if value, ok := req.Metadata[networkMetadataKey].(bool); ok {
		policy.AllowNetwork = value
	}
	command, err := BuildArgs(policy, []string{a.shell, "-c", ""}, workingDir)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", sandbox.ErrSessionOpenFailed, err)
	}
	// Drop the placeholder command, keeping the terminating "--", so each
	// execution appends its own command.
	prefix := append([]string(nil), command.Args[:len(command.Args)-3]...)

	env := map[string]string{}
	for name, value := range req.Env {
		env[name] = value
	}
	digest := sha256.Sum256([]byte(strings.Join(command.Args, "\x00")))
	a.mu.Lock()
	a.sessions[key] = &session{workingDir: workingDir, prefix: prefix, env: env}
	a.mu.Unlock()

	return &sandbox.Session{
		ID:            key,
		RunID:         req.RunID,
		SubtaskID:     req.SubtaskID,
		EnvironmentID: req.EnvironmentID,
		WorkingDir:    workingDir,
		Metadata: map[string]any{
			"backend":            "bwrap",
			argsMetadataKey:      command.Args,
			argsHashMetadataKey:  hex.EncodeToString(digest[:]),
			writableRootsMetaKey: existing,
		},
	}, nil
}

// resolveWorkingDir picks and validates the session's working directory.
func (a *Adapter) resolveWorkingDir(req sandbox.OpenRequest) (string, error) {
	dir := strings.TrimSpace(req.WorkingDir)
	if dir == "" {
		dir = a.defaultDir
	}
	if dir == "" {
		current, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("%w: %v", sandbox.ErrSessionOpenFailed, err)
		}
		dir = current
	}
	if !filepath.IsAbs(dir) {
		return "", fmt.Errorf("%w: working directory %q must be absolute", sandbox.ErrSessionOpenFailed, dir)
	}
	return filepath.Clean(dir), nil
}

// Execute runs one command inside the session's bubblewrap layout.
func (a *Adapter) Execute(ctx context.Context, session *sandbox.Session, req sandbox.ExecuteRequest) (sandbox.ExecuteResult, error) {
	if session == nil {
		return sandbox.ExecuteResult{}, fmt.Errorf("%w: session is nil", sandbox.ErrSessionOpenFailed)
	}
	a.mu.Lock()
	active, ok := a.sessions[session.ID]
	resolved := a.resolved
	a.mu.Unlock()
	if !ok {
		return sandbox.ExecuteResult{}, fmt.Errorf("%w: session %s is unknown", sandbox.ErrClosed, session.ID)
	}
	command := strings.TrimSpace(req.Command)
	if command == "" {
		return sandbox.ExecuteResult{}, fmt.Errorf("%w: command is required", sandbox.ErrExecuteFailed)
	}
	workingDir := strings.TrimSpace(req.CWD)
	if workingDir == "" {
		workingDir = active.workingDir
	}
	if !filepath.IsAbs(workingDir) {
		return sandbox.ExecuteResult{}, fmt.Errorf("%w: cwd %q must be absolute", sandbox.ErrExecuteFailed, workingDir)
	}
	timeout := req.Timeout
	if timeout <= 0 {
		timeout = a.timeout
	}

	// The layout carries the cwd because bubblewrap chdirs before running
	// the command; a per-call cwd replaces it.
	seq := append([]string(nil), active.prefix...)
	if index := indexOf(seq, "--chdir"); index >= 0 && index+1 < len(seq) {
		seq[index+1] = workingDir
	}
	seq = append(seq, a.shell, "-c", command)

	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var stdout, stderr bytes.Buffer
	runner := a.runner
	if runner == nil {
		executable := a.executable
		if resolved != "" {
			executable = resolved
		}
		_ = executable
		runner = &execRunner{
			dir: active.workingDir,
			env: mergeEnv(active.env, req.Env),
		}
	}
	err := runner.Run(runCtx, a.executablePath(resolved), seq, &stdout, &stderr)
	result := sandbox.ExecuteResult{
		Stdout:           stdout.String(),
		Stderr:           stderr.String(),
		WorkingDirectory: workingDir,
		Metadata: map[string]any{
			"backend":           "bwrap",
			"executable":        a.executablePath(resolved),
			argsHashMetadataKey: session.Metadata[argsHashMetadataKey],
		},
	}
	if int64(stdout.Len()) > a.maxOutput || int64(stderr.Len()) > a.maxOutput {
		return result, fmt.Errorf("%w: output exceeded %d bytes", sandbox.ErrResponseTooLarge, a.maxOutput)
	}
	if err != nil {
		var exitErr *exec.ExitError
		switch {
		case errors.Is(runCtx.Err(), context.DeadlineExceeded):
			return result, fmt.Errorf("%w: command exceeded %s", sandbox.ErrTimeout, timeout)
		case errors.As(err, &exitErr):
			if exitErr.ExitCode() < 0 {
				return result, fmt.Errorf("%w: sandbox runner terminated by signal: %v", sandbox.ErrExecuteFailed, err)
			}
			result.ExitCode = exitErr.ExitCode()
			result.Metadata["exitCode"] = result.ExitCode
			return result, nil
		default:
			return result, fmt.Errorf("%w: %v", sandbox.ErrExecuteFailed, err)
		}
	}
	return result, nil
}

// Close forgets a session.
func (a *Adapter) Close(ctx context.Context, session *sandbox.Session) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if session == nil {
		return fmt.Errorf("%w: session is nil", sandbox.ErrClosed)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, ok := a.sessions[session.ID]; !ok {
		return fmt.Errorf("%w: session %s is unknown", sandbox.ErrClosed, session.ID)
	}
	delete(a.sessions, session.ID)
	return nil
}

// executablePath returns the resolved runner path.
func (a *Adapter) executablePath(resolved string) string {
	if resolved != "" {
		return resolved
	}
	return a.executable
}

// execRunner runs the real bubblewrap process.
type execRunner struct {
	dir string
	env []string
}

func (r *execRunner) Run(ctx context.Context, executable string, args []string, stdout, stderr io.Writer) error {
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.Dir = r.dir
	cmd.Env = r.env
	cmd.Stdout = limitWriter(stdout, internalOutputLimit)
	cmd.Stderr = limitWriter(stderr, internalOutputLimit)
	return cmd.Run()
}

// limitWriter caps how much a child may write to one stream.
func limitWriter(w io.Writer, limit int64) io.Writer {
	if limit <= 0 {
		return w
	}
	return &boundedWriter{w: w, remaining: limit}
}

type boundedWriter struct {
	w         io.Writer
	remaining int64
}

func (b *boundedWriter) Write(p []byte) (int, error) {
	if b.remaining <= 0 {
		return len(p), nil
	}
	if int64(len(p)) > b.remaining {
		p = p[:b.remaining]
	}
	written, err := b.w.Write(p)
	b.remaining -= int64(written)
	if err != nil {
		return written, err
	}
	return len(p), nil
}

// mergeEnv overlays the call's environment on the session's and the host's,
// so a sandboxed toolchain still finds its binaries and home directory.
func mergeEnv(sessionEnv, callEnv map[string]string) []string {
	merged := map[string]string{}
	for _, entry := range os.Environ() {
		if name, value, ok := strings.Cut(entry, "="); ok {
			merged[name] = value
		}
	}
	for name, value := range sessionEnv {
		merged[name] = value
	}
	for name, value := range callEnv {
		merged[name] = value
	}
	out := make([]string, 0, len(merged))
	for name, value := range merged {
		out = append(out, name+"="+value)
	}
	return out
}

// metadataStringList reads a string list from the request metadata.
func metadataStringList(metadata map[string]any, key string) []string {
	if metadata == nil {
		return nil
	}
	switch value := metadata[key].(type) {
	case []string:
		return value
	case []any:
		out := make([]string, 0, len(value))
		for _, item := range value {
			if text, ok := item.(string); ok {
				out = append(out, text)
			}
		}
		return out
	default:
		return nil
	}
}

// indexOf returns the index of value, or -1.
func indexOf(values []string, value string) int {
	for index, candidate := range values {
		if candidate == value {
			return index
		}
	}
	return -1
}
