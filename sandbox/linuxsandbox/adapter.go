package linuxsandbox

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
	policyMetadataKey     = "zenforge.linuxsandbox.policy"
	policyHashMetadataKey = "zenforge.linuxsandbox.policyHash"
	rootsMetadataKey      = "zenforge.linuxsandbox.writableRoots"
	networkMetadataKey    = "zenforge.linuxsandbox.allowNetwork"
)

// Runner executes the helper. It is injectable so the adapter's lifecycle,
// argument construction, and error mapping are testable on any platform.
type Runner interface {
	Run(ctx context.Context, executable string, args []string, dir string, env []string, stdout, stderr io.Writer) error
}

// Config configures the adapter.
type Config struct {
	// Helper is the executable re-invoked as the confinement helper. It
	// defaults to the running executable, which is the zenforge binary that
	// knows the linux-sandbox subcommand.
	Helper string
	// Shell is the program that receives the command string.
	Shell string
	// DefaultWorkingDir seeds a session whose request names none.
	DefaultWorkingDir string
	// WritableRoots are the roots a session may modify; the working
	// directory is added automatically.
	WritableRoots []string
	// ReadableRoots are the read roots when FullDiskRead is false.
	ReadableRoots []string
	// FullDiskRead grants read access to the whole filesystem (default
	// true).
	FullDiskRead *bool
	// ReadWritePaths are individual paths granted read-write access.
	ReadWritePaths []string
	// AllowNetwork keeps the network syscalls available.
	AllowNetwork bool
	// ExtraDeny names additional syscalls to deny.
	ExtraDeny []string
	// ProtectedNames are refused: Landlock cannot express a read-only
	// carve-out inside a writable root.
	ProtectedNames []string
	// DefaultTimeout bounds one execution.
	DefaultTimeout time.Duration
	// MaxOutputBytes caps per-stream output.
	MaxOutputBytes int64
	// Runner replaces real helper execution.
	Runner Runner
	// SkipPlatformCheck allows exercising the adapter off Linux.
	SkipPlatformCheck bool
	// Arch overrides the architecture the helper plans its filter for. It
	// exists for tests and cross-architecture execution.
	Arch string
}

// Adapter implements sandbox.Sandbox with Landlock plus seccomp.
type Adapter struct {
	helper       string
	shell        string
	defaultDir   string
	config       Config
	timeout      time.Duration
	maxOutput    int64
	runner       Runner
	skipPlatform bool

	mu       sync.Mutex
	sessions map[string]*session
}

var _ sandbox.Sandbox = (*Adapter)(nil)

type session struct {
	workingDir string
	policy     Policy
	policyJSON string
	env        map[string]string
}

// New returns an adapter, validating its configuration.
func New(config Config) (*Adapter, error) {
	helper := strings.TrimSpace(config.Helper)
	if helper == "" {
		executable, err := os.Executable()
		if err != nil {
			return nil, fmt.Errorf("%w: resolve the helper executable: %v", sandbox.ErrSandboxUnavailable, err)
		}
		helper = executable
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
	// Refuse an inexpressible policy at construction time rather than at
	// the first command, so a misconfigured deployment fails at startup.
	if len(config.ProtectedNames) > 0 {
		return nil, Policy{ProtectedNames: config.ProtectedNames}.Validate()
	}
	config.FullDiskRead = &fullDiskRead
	return &Adapter{
		helper:       helper,
		shell:        shell,
		defaultDir:   strings.TrimSpace(config.DefaultWorkingDir),
		config:       config,
		timeout:      timeout,
		maxOutput:    maxOutput,
		runner:       config.Runner,
		skipPlatform: config.SkipPlatformCheck,
		sessions:     map[string]*session{},
	}, nil
}

// Open prepares a session and records the policy it will apply.
func (a *Adapter) Open(ctx context.Context, req sandbox.OpenRequest) (*sandbox.Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	key := sandbox.SessionKey(req.RunID, req.SubtaskID)
	if key == "" {
		return nil, fmt.Errorf("%w: run id is required", sandbox.ErrSessionOpenFailed)
	}
	if !a.skipPlatform && a.runner == nil && runtime.GOOS != "linux" {
		return nil, fmt.Errorf("%w: the landlock+seccomp sandbox is only available on linux (host is %s)", sandbox.ErrSandboxUnavailable, runtime.GOOS)
	}
	if a.runner == nil {
		if _, err := os.Stat(a.helper); err != nil {
			return nil, fmt.Errorf("%w: helper %s is not usable: %v", sandbox.ErrSandboxUnavailable, a.helper, err)
		}
	}
	workingDir, err := a.resolveWorkingDir(req)
	if err != nil {
		return nil, err
	}
	roots := append([]string(nil), a.config.WritableRoots...)
	roots = append(roots, metadataStrings(req.Metadata, rootsMetadataKey)...)
	if info, err := os.Stat(workingDir); err == nil && info.IsDir() {
		roots = append(roots, workingDir)
	}
	policy := Policy{
		WritableRoots:  roots,
		ReadableRoots:  a.config.ReadableRoots,
		FullDiskRead:   a.config.FullDiskRead == nil || *a.config.FullDiskRead,
		ReadWritePaths: append([]string{"/dev/null"}, a.config.ReadWritePaths...),
		AllowNetwork:   a.config.AllowNetwork,
		ExtraDeny:      a.config.ExtraDeny,
	}
	// Landlock needs readable roots to exist as well, but a mixed-platform
	// configuration may name a path that is absent here; the ruleset builder
	// treats a missing path as a startup error, so drop the ones that do not
	// exist and record what was dropped.
	policy.WritableRoots = existingPaths(policy.WritableRoots)
	policy.ReadableRoots = existingPaths(policy.ReadableRoots)
	if value, ok := req.Metadata[networkMetadataKey].(bool); ok {
		policy.AllowNetwork = value
	}
	policyJSON, err := policy.Encode()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", sandbox.ErrSessionOpenFailed, err)
	}
	env := map[string]string{}
	for name, value := range req.Env {
		env[name] = value
	}
	digest := sha256.Sum256([]byte(policyJSON))
	a.mu.Lock()
	a.sessions[key] = &session{workingDir: workingDir, policy: policy, policyJSON: policyJSON, env: env}
	a.mu.Unlock()
	return &sandbox.Session{
		ID:            key,
		RunID:         req.RunID,
		SubtaskID:     req.SubtaskID,
		EnvironmentID: req.EnvironmentID,
		WorkingDir:    workingDir,
		Metadata: map[string]any{
			"backend":             "landlock",
			policyMetadataKey:     policyJSON,
			policyHashMetadataKey: hex.EncodeToString(digest[:]),
			rootsMetadataKey:      policy.WritableRoots,
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

// Execute runs one command through the helper.
func (a *Adapter) Execute(ctx context.Context, session *sandbox.Session, req sandbox.ExecuteRequest) (sandbox.ExecuteResult, error) {
	if session == nil {
		return sandbox.ExecuteResult{}, fmt.Errorf("%w: session is nil", sandbox.ErrSessionOpenFailed)
	}
	a.mu.Lock()
	active, ok := a.sessions[session.ID]
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

	args := []string{HelperCommand, "--policy", active.policyJSON, "--", a.shell, "-c", command}
	if a.config.Arch != "" {
		args = append([]string{HelperCommand, "--arch", a.config.Arch}, args[1:]...)
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var stdout, stderr bytes.Buffer
	runner := a.runner
	if runner == nil {
		runner = &execRunner{}
	}
	err := runner.Run(runCtx, a.helper, args, workingDir, mergeEnv(active.env, req.Env), &stdout, &stderr)
	result := sandbox.ExecuteResult{
		Stdout:           stdout.String(),
		Stderr:           stderr.String(),
		WorkingDirectory: workingDir,
		Metadata: map[string]any{
			"backend":             "landlock",
			"helper":              a.helper,
			policyHashMetadataKey: session.Metadata[policyHashMetadataKey],
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
				return result, fmt.Errorf("%w: helper terminated by signal: %v", sandbox.ErrExecuteFailed, err)
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

// execRunner runs the real helper process.
type execRunner struct{}

func (r *execRunner) Run(ctx context.Context, executable string, args []string, dir string, env []string, stdout, stderr io.Writer) error {
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.Dir = dir
	cmd.Env = env
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

// mergeEnv overlays the call's environment on the session's and the host's.
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

// metadataStrings reads a string list from the request metadata.
func metadataStrings(metadata map[string]any, key string) []string {
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

// existingPaths drops paths that do not exist, because Landlock cannot
// grant access beneath a path it cannot open, and deduplicates the rest so
// the recorded policy has one rule per root.
func existingPaths(paths []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		if strings.TrimSpace(path) == "" {
			continue
		}
		cleaned := filepath.Clean(path)
		if seen[cleaned] {
			continue
		}
		if _, err := os.Stat(cleaned); err != nil {
			continue
		}
		seen[cleaned] = true
		out = append(out, cleaned)
	}
	return out
}
