package seatbelt

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
	defaultExecutable      = "sandbox-exec"
	defaultShell           = "/bin/sh"
	defaultTimeout         = 30 * time.Second
	defaultMaxOutputBytes  = int64(8 << 20)
	internalOutputLimit    = int64(64 << 10)
	profileMetadataKey     = "zenforge.seatbelt.profile"
	paramsMetadataKey      = "zenforge.seatbelt.params"
	writableRootsMetaKey   = "zenforge.seatbelt.writableRoots"
	readableRootsMetaKey   = "zenforge.seatbelt.readableRoots"
	readOnlyPathsMetaKey   = "zenforge.seatbelt.readOnlyPaths"
	networkMetadataKey     = "zenforge.seatbelt.allowNetwork"
	profileHashMetadataKey = "zenforge.seatbelt.profileHash"
)

// Runner executes the sandbox executable. It is injectable so tests can
// exercise policy, lifecycle, and error mapping without macOS Seatbelt.
type Runner interface {
	Run(ctx context.Context, executable string, args []string, stdout, stderr io.Writer) error
}

// Config configures the adapter.
type Config struct {
	// Executable is the sandbox runner, "sandbox-exec" by default.
	Executable string
	// Shell is the program that receives the command string, "/bin/sh" by
	// default.
	Shell string
	// DefaultWorkingDir seeds a session whose request names none.
	DefaultWorkingDir string
	// WritableRoots are the roots a session may modify. The working
	// directory is added automatically unless IncludeWorkingDir is set to
	// an explicit false.
	WritableRoots []string
	// ReadableRoots are extra read-only roots.
	ReadableRoots []string
	// ReadOnlyPaths are paths pinned read-only inside writable roots.
	ReadOnlyPaths []string
	// ProtectedNames selects the protected basenames inside writable roots.
	ProtectedNames []string
	// AllowNetwork grants outbound network access.
	AllowNetwork bool
	// AllowLocalBinding grants network-bind.
	AllowLocalBinding bool
	// ExtraPolicy is appended to every generated profile.
	ExtraPolicy string
	// DefaultTimeout bounds one execution.
	DefaultTimeout time.Duration
	// MaxOutputBytes caps combined per-stream output.
	MaxOutputBytes int64
	// Runner replaces the real process execution.
	Runner Runner
	// LookPath resolves the executable; nil uses exec.LookPath.
	LookPath func(string) (string, error)
	// SkipPlatformCheck allows exercising the adapter off macOS. It is for
	// tests and for hosts that run the sandbox through a remote runner.
	SkipPlatformCheck bool
}

// Adapter implements sandbox.Sandbox with macOS Seatbelt.
type Adapter struct {
	executable        string
	shell             string
	defaultWorkingDir string
	writableRoots     []string
	readableRoots     []string
	readOnlyPaths     []string
	protectedNames    []string
	allowNetwork      bool
	allowLocalBinding bool
	extraPolicy       string
	timeout           time.Duration
	maxOutput         int64
	runner            Runner
	lookPath          func(string) (string, error)
	skipPlatformCheck bool

	mu       sync.Mutex
	resolved string
	sessions map[string]*session
}

var _ sandbox.Sandbox = (*Adapter)(nil)

type session struct {
	workingDir string
	profile    BuiltPolicy
	env        map[string]string
	closed     bool
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
	roots, err := normalizeRoots(config.WritableRoots, "writable root")
	if err != nil {
		return nil, err
	}
	readable, err := normalizeRoots(config.ReadableRoots, "readable root")
	if err != nil {
		return nil, err
	}
	readOnly, err := normalizeRoots(config.ReadOnlyPaths, "read-only path")
	if err != nil {
		return nil, err
	}
	for _, name := range config.ProtectedNames {
		if strings.TrimSpace(name) == "" || strings.ContainsAny(name, `/\`) {
			return nil, fmt.Errorf("seatbelt protected name %q must be a plain basename", name)
		}
	}
	lookPath := config.LookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	return &Adapter{
		executable:        executable,
		shell:             shell,
		defaultWorkingDir: strings.TrimSpace(config.DefaultWorkingDir),
		writableRoots:     roots,
		readableRoots:     readable,
		readOnlyPaths:     readOnly,
		protectedNames:    config.ProtectedNames,
		allowNetwork:      config.AllowNetwork,
		allowLocalBinding: config.AllowLocalBinding,
		extraPolicy:       config.ExtraPolicy,
		timeout:           timeout,
		maxOutput:         maxOutput,
		runner:            config.Runner,
		lookPath:          lookPath,
		skipPlatformCheck: config.SkipPlatformCheck,
		sessions:          map[string]*session{},
	}, nil
}

// Open prepares a session: it resolves the working directory, builds the
// policy, and checks that the runner exists. The profile is stored on the
// session so a resume rebuilds nothing and the same policy governs every
// execution of the session.
func (a *Adapter) Open(ctx context.Context, req sandbox.OpenRequest) (*sandbox.Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	key := sandbox.SessionKey(req.RunID, req.SubtaskID)
	if key == "" {
		return nil, fmt.Errorf("%w: run id is required", sandbox.ErrSessionOpenFailed)
	}
	if !a.skipPlatformCheck && a.runner == nil && runtime.GOOS != "darwin" {
		return nil, fmt.Errorf("%w: seatbelt is only available on darwin (host is %s)", sandbox.ErrSandboxUnavailable, runtime.GOOS)
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
	roots := append([]string(nil), a.writableRoots...)
	roots = append(roots, metadataStringList(req.Metadata, writableRootsMetaKey)...)
	if info, err := os.Stat(workingDir); err == nil && info.IsDir() {
		roots = append(roots, workingDir)
	}
	roots, err = normalizeRoots(roots, "writable root")
	if err != nil {
		return nil, fmt.Errorf("%w: %v", sandbox.ErrSessionOpenFailed, err)
	}
	policy := Policy{
		WritableRoots:  roots,
		ReadableRoots:  append(append([]string(nil), a.readableRoots...), metadataStringList(req.Metadata, readableRootsMetaKey)...),
		ReadOnlyPaths:  append(append([]string(nil), a.readOnlyPaths...), metadataStringList(req.Metadata, readOnlyPathsMetaKey)...),
		ProtectedNames: a.protectedNames,
		AllowNetwork:   a.allowNetwork,
		ExtraRules:     a.extraPolicy,
	}
	if value, ok := req.Metadata[networkMetadataKey].(bool); ok {
		policy.AllowNetwork = value
	}
	policy.AllowLocalBinding = a.allowLocalBinding
	built, err := BuildPolicy(policy)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", sandbox.ErrSessionOpenFailed, err)
	}

	env := map[string]string{}
	for name, value := range req.Env {
		env[name] = value
	}
	digest := sha256.Sum256([]byte(built.Profile))
	a.mu.Lock()
	a.sessions[key] = &session{workingDir: workingDir, profile: built, env: env}
	a.mu.Unlock()

	return &sandbox.Session{
		ID:            key,
		RunID:         req.RunID,
		SubtaskID:     req.SubtaskID,
		EnvironmentID: req.EnvironmentID,
		WorkingDir:    workingDir,
		Metadata: map[string]any{
			"backend":              "seatbelt",
			profileMetadataKey:     built.Profile,
			paramsMetadataKey:      built.Params,
			profileHashMetadataKey: hex.EncodeToString(digest[:]),
		},
	}, nil
}

// resolveWorkingDir picks and validates the session's working directory.
func (a *Adapter) resolveWorkingDir(req sandbox.OpenRequest) (string, error) {
	dir := strings.TrimSpace(req.WorkingDir)
	if dir == "" {
		dir = a.defaultWorkingDir
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
	return canonicalPath(dir), nil
}

// Execute runs one command inside the session's profile.
func (a *Adapter) Execute(ctx context.Context, session *sandbox.Session, req sandbox.ExecuteRequest) (sandbox.ExecuteResult, error) {
	if session == nil {
		return sandbox.ExecuteResult{}, fmt.Errorf("%w: session is nil", sandbox.ErrSessionOpenFailed)
	}
	a.mu.Lock()
	active, ok := a.sessions[session.ID]
	if !ok {
		a.mu.Unlock()
		return sandbox.ExecuteResult{}, fmt.Errorf("%w: session %s is unknown", sandbox.ErrClosed, session.ID)
	}
	if active.closed {
		a.mu.Unlock()
		return sandbox.ExecuteResult{}, fmt.Errorf("%w: session %s is closed", sandbox.ErrClosed, session.ID)
	}
	resolved := a.resolved
	a.mu.Unlock()

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

	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	args := append(active.profile.Argument(), a.shell, "-c", command)
	var stdout, stderr bytes.Buffer
	runner := a.runner
	if runner == nil {
		runner = &execRunner{
			dir: workingDir,
			env: mergeEnv(active.env, req.Env),
		}
	}
	err := runner.Run(runCtx, a.executablePath(resolved), args, &stdout, &stderr)
	result := sandbox.ExecuteResult{
		Stdout:           stdout.String(),
		Stderr:           stderr.String(),
		WorkingDirectory: workingDir,
		Metadata: map[string]any{
			"backend":              "seatbelt",
			"executable":           a.executablePath(resolved),
			profileHashMetadataKey: session.Metadata[profileHashMetadataKey],
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
			result.ExitCode = exitErr.ExitCode()
			result.Metadata["exitCode"] = result.ExitCode
			return result, nil
		default:
			return result, fmt.Errorf("%w: %v", sandbox.ErrExecuteFailed, err)
		}
	}
	return result, nil
}

// executablePath returns the resolved runner path.
func (a *Adapter) executablePath(resolved string) string {
	if resolved != "" {
		return resolved
	}
	return a.executable
}

// Close forgets a session. A closed session cannot execute again.
func (a *Adapter) Close(ctx context.Context, session *sandbox.Session) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if session == nil {
		return fmt.Errorf("%w: session is nil", sandbox.ErrClosed)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	active, ok := a.sessions[session.ID]
	if !ok {
		return fmt.Errorf("%w: session %s is unknown", sandbox.ErrClosed, session.ID)
	}
	active.closed = true
	delete(a.sessions, session.ID)
	return nil
}

// execRunner runs the real command through os/exec.
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

// mergeEnv overlays the call's environment on the session's, and always
// includes the host environment so a sandboxed toolchain can find its
// binaries and home directory.
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
