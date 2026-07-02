package docker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/feiyu912/zenforge/sandbox"
)

var envNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

const (
	mountsMetadataKey   = "zenforge.docker.mounts"
	internalOutputLimit = int64(64 << 10)
)

type Adapter struct {
	cli        string
	image      string
	workingDir string
	timeout    time.Duration
	maxOutput  int64
	network    string
	writable   bool
	pidsLimit  int
	runner     Runner

	mu       sync.Mutex
	sessions map[string]struct{}
}

var _ sandbox.Sandbox = (*Adapter)(nil)

func New(config Config) (*Adapter, error) {
	cli := strings.TrimSpace(config.DockerCLI)
	if cli == "" {
		cli = "docker"
	}
	image := strings.TrimSpace(config.DefaultImage)
	if image == "" {
		image = defaultImage
	}
	workingDir := strings.TrimSpace(config.DefaultWorkingDir)
	if workingDir == "" {
		workingDir = defaultWorkingDir
	}
	if err := validateContainerPath(workingDir, "default working directory"); err != nil {
		return nil, err
	}
	timeout := config.DefaultTimeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	maxOutput := config.MaxOutputBytes
	if maxOutput <= 0 {
		maxOutput = defaultMaxOutputBytes
	}
	network := strings.TrimSpace(config.NetworkMode)
	if network == "" {
		network = "none"
	}
	pidsLimit := config.PidsLimit
	if pidsLimit <= 0 {
		pidsLimit = 256
	}
	runner := config.Runner
	if runner == nil {
		runner = execRunner{}
	}
	return &Adapter{
		cli: cli, image: image, workingDir: workingDir, timeout: timeout,
		maxOutput: maxOutput, network: network, writable: config.WritableRootFS,
		pidsLimit: pidsLimit, runner: runner, sessions: make(map[string]struct{}),
	}, nil
}

func (a *Adapter) Open(ctx context.Context, req sandbox.OpenRequest) (*sandbox.Session, error) {
	runID := strings.TrimSpace(req.RunID)
	if runID == "" {
		return nil, fmt.Errorf("%w: run id is required", sandbox.ErrSessionOpenFailed)
	}
	image := strings.TrimSpace(req.EnvironmentID)
	if image == "" {
		image = a.image
	}
	mounts, err := validateMounts(req.Mounts)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", sandbox.ErrSessionOpenFailed, err)
	}
	if err := validateEnv(req.Env); err != nil {
		return nil, fmt.Errorf("%w: %v", sandbox.ErrSessionOpenFailed, err)
	}
	cwd := mapContainerCWD(req.WorkingDir, mounts, a.workingDir)
	id := containerName(runID, strings.TrimSpace(req.SubtaskID))
	openCtx, cancel := context.WithTimeout(ctx, a.timeout)
	defer cancel()
	existing := &sandbox.Session{ID: id, RunID: runID, SubtaskID: strings.TrimSpace(req.SubtaskID)}
	exists, running, err := a.inspectSession(openCtx, existing)
	if err != nil {
		return nil, a.mapError(openCtx, err, sandbox.ErrSessionOpenFailed, "inspect existing container")
	}
	if exists {
		if running {
			return nil, fmt.Errorf("%w: container %q is already running", sandbox.ErrSessionOpenFailed, id)
		}
		if _, _, err := a.run(openCtx, internalOutputLimit, "rm", "-f", id); err != nil {
			return nil, a.mapError(openCtx, err, sandbox.ErrSessionOpenFailed, "remove existing container")
		}
	}
	args := []string{"create", "--name", id,
		"--label", "zenforge.run_id=" + runID,
		"--label", "zenforge.subtask_id=" + strings.TrimSpace(req.SubtaskID),
		"--network", a.network, "--cap-drop", "ALL", "--security-opt", "no-new-privileges",
		"--pids-limit", strconv.Itoa(a.pidsLimit), "--workdir", cwd}
	if !a.writable {
		args = append(args, "--read-only", "--tmpfs", "/tmp:rw,noexec,nosuid,size=64m")
	}
	for _, mount := range mounts {
		args = append(args, "--mount", mountArg(mount))
	}
	args = appendEnv(args, req.Env)
	args = append(args, image, "tail", "-f", "/dev/null")
	if _, _, err := a.run(openCtx, internalOutputLimit, args...); err != nil {
		return nil, a.mapError(openCtx, err, sandbox.ErrSessionOpenFailed, "create container")
	}
	if _, _, err := a.run(openCtx, internalOutputLimit, "start", id); err != nil {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), a.timeout)
		defer cleanupCancel()
		_, _, _ = a.run(cleanupCtx, internalOutputLimit, "rm", "-f", id)
		return nil, a.mapError(openCtx, err, sandbox.ErrSessionOpenFailed, "start container")
	}
	a.mu.Lock()
	a.sessions[id] = struct{}{}
	a.mu.Unlock()
	metadata := cloneMap(req.Metadata)
	if metadata == nil {
		metadata = make(map[string]any, 1)
	}
	metadata[mountsMetadataKey] = cloneMounts(mounts)
	return &sandbox.Session{
		ID: id, RunID: runID, SubtaskID: strings.TrimSpace(req.SubtaskID),
		EnvironmentID: image, WorkingDir: cwd, Metadata: metadata,
	}, nil
}

func (a *Adapter) Execute(ctx context.Context, session *sandbox.Session, req sandbox.ExecuteRequest) (sandbox.ExecuteResult, error) {
	if err := a.ensureOpen(ctx, session); err != nil {
		return sandbox.ExecuteResult{}, err
	}
	if err := validateEnv(req.Env); err != nil {
		return sandbox.ExecuteResult{}, fmt.Errorf("%w: %v", sandbox.ErrExecuteFailed, err)
	}
	timeout := req.Timeout
	if timeout <= 0 {
		timeout = a.timeout
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cwd := strings.TrimSpace(req.CWD)
	cwd = mapContainerCWD(cwd, mountsFromMetadata(session.Metadata), session.WorkingDir)
	args := []string{"exec", "--workdir", cwd}
	args = appendEnv(args, req.Env)
	args = append(args, session.ID, "/bin/sh", "-lc", req.Command)
	stdout, stderr, err := a.run(runCtx, a.maxOutput, args...)
	result := sandbox.ExecuteResult{
		ExitCode: exitCode(err), Stdout: stdout, Stderr: stderr,
		WorkingDirectory: cwd, Metadata: cloneMap(req.Metadata),
	}
	if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		result.ExitCode = 124
		return result, fmt.Errorf("%w: command exceeded %s", sandbox.ErrTimeout, timeout)
	}
	if errors.Is(runCtx.Err(), context.Canceled) {
		return result, context.Canceled
	}
	if errors.Is(err, errOutputLimit) {
		return result, fmt.Errorf("%w: output exceeds %d byte limit", sandbox.ErrResponseTooLarge, a.maxOutput)
	}
	var exitErr *exec.ExitError
	if err == nil || errors.As(err, &exitErr) {
		return result, nil
	}
	return result, a.mapError(runCtx, err, sandbox.ErrExecuteFailed, "execute command")
}

func (a *Adapter) Close(ctx context.Context, session *sandbox.Session) error {
	closeCtx, cancel := context.WithTimeout(ctx, a.timeout)
	defer cancel()
	if err := a.ensureOpen(closeCtx, session); err != nil {
		return err
	}
	if _, _, err := a.run(closeCtx, internalOutputLimit, "rm", "-f", session.ID); err != nil {
		return a.mapError(closeCtx, err, sandbox.ErrSandboxUnavailable, "remove container")
	}
	a.mu.Lock()
	delete(a.sessions, session.ID)
	a.mu.Unlock()
	return nil
}

func (a *Adapter) ensureOpen(ctx context.Context, session *sandbox.Session) error {
	if session == nil || strings.TrimSpace(session.ID) == "" {
		return sandbox.ErrClosed
	}
	a.mu.Lock()
	_, ok := a.sessions[session.ID]
	a.mu.Unlock()
	if ok {
		return nil
	}
	exists, running, err := a.inspectSession(ctx, session)
	if err != nil {
		return a.mapError(ctx, err, sandbox.ErrSandboxUnavailable, "inspect container")
	}
	if !exists || !running {
		return sandbox.ErrClosed
	}
	a.mu.Lock()
	a.sessions[session.ID] = struct{}{}
	a.mu.Unlock()
	return nil
}

type inspectResult struct {
	Running bool              `json:"running"`
	Labels  map[string]string `json:"labels"`
}

func (a *Adapter) inspectSession(ctx context.Context, session *sandbox.Session) (bool, bool, error) {
	format := `{"running":{{.State.Running}},"labels":{{json .Config.Labels}}}`
	stdout, stderr, err := a.run(ctx, internalOutputLimit, "inspect", "--format", format, session.ID)
	if err != nil {
		if isMissingContainer(stderr) {
			return false, false, nil
		}
		return false, false, err
	}
	var inspected inspectResult
	if err := json.Unmarshal([]byte(stdout), &inspected); err != nil {
		return false, false, fmt.Errorf("decode docker inspect response: %w", err)
	}
	if inspected.Labels["zenforge.run_id"] != strings.TrimSpace(session.RunID) ||
		inspected.Labels["zenforge.subtask_id"] != strings.TrimSpace(session.SubtaskID) {
		return false, false, fmt.Errorf("container %q ownership labels do not match session", session.ID)
	}
	return true, inspected.Running, nil
}

func isMissingContainer(stderr string) bool {
	stderr = strings.ToLower(stderr)
	return strings.Contains(stderr, "no such object") || strings.Contains(stderr, "no such container")
}

var errOutputLimit = errors.New("docker output limit exceeded")

func (a *Adapter) run(ctx context.Context, limit int64, args ...string) (string, string, error) {
	capture := newCapture(limit)
	err := a.runner.Run(ctx, a.cli, args, capture.stdout(), capture.stderr())
	if capture.exceeded() {
		return capture.strings(errOutputLimit)
	}
	stdout, stderr, _ := capture.strings(nil)
	return stdout, stderr, err
}

func (a *Adapter) mapError(ctx context.Context, err error, fallback sandbox.ErrorCode, action string) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%w: %s: %v", sandbox.ErrTimeout, action, err)
	}
	if errors.Is(ctx.Err(), context.Canceled) || errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: docker CLI %q unavailable: %v", sandbox.ErrSandboxUnavailable, a.cli, err)
	}
	if errors.Is(err, errOutputLimit) {
		return fmt.Errorf("%w: %s output exceeds limit", sandbox.ErrResponseTooLarge, action)
	}
	return fmt.Errorf("%w: %s: %v", fallback, action, err)
}

func mapContainerCWD(requested string, mounts []sandbox.Mount, fallback string) string {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return fallback
	}
	hostPath, err := filepath.Abs(requested)
	if err == nil {
		for _, mount := range mounts {
			source, absErr := filepath.Abs(mount.Source)
			if absErr != nil {
				continue
			}
			rel, relErr := filepath.Rel(source, hostPath)
			if relErr == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				if rel == "." {
					return mount.Destination
				}
				return filepath.ToSlash(filepath.Join(mount.Destination, rel))
			}
		}
	}
	if validateContainerPath(requested, "working directory") == nil &&
		(requested == fallback || hasDestinationPrefix(requested, mounts)) {
		return filepath.ToSlash(filepath.Clean(requested))
	}
	return fallback
}

func cloneMounts(mounts []sandbox.Mount) []sandbox.Mount {
	return append([]sandbox.Mount(nil), mounts...)
}

func mountsFromMetadata(metadata map[string]any) []sandbox.Mount {
	if metadata == nil {
		return nil
	}
	switch value := metadata[mountsMetadataKey].(type) {
	case []sandbox.Mount:
		return cloneMounts(value)
	case []any:
		out := make([]sandbox.Mount, 0, len(value))
		for _, item := range value {
			raw, ok := item.(map[string]any)
			if !ok {
				continue
			}
			source, _ := raw["source"].(string)
			destination, _ := raw["destination"].(string)
			mode, _ := raw["mode"].(string)
			out = append(out, sandbox.Mount{Source: source, Destination: destination, Mode: mode})
		}
		return out
	default:
		return nil
	}
}

func validateMounts(mounts []sandbox.Mount) ([]sandbox.Mount, error) {
	out := make([]sandbox.Mount, 0, len(mounts))
	destinations := make(map[string]struct{}, len(mounts))
	for _, mount := range mounts {
		source, err := filepath.Abs(strings.TrimSpace(mount.Source))
		if err != nil || strings.TrimSpace(mount.Source) == "" {
			return nil, fmt.Errorf("mount source %q is invalid", mount.Source)
		}
		if _, err := os.Stat(source); err != nil {
			return nil, fmt.Errorf("mount source %q: %v", source, err)
		}
		destination := filepath.ToSlash(filepath.Clean(strings.TrimSpace(mount.Destination)))
		if err := validateContainerPath(destination, "mount destination"); err != nil {
			return nil, err
		}
		if _, exists := destinations[destination]; exists {
			return nil, fmt.Errorf("duplicate mount destination %q", destination)
		}
		destinations[destination] = struct{}{}
		mode := strings.ToLower(strings.TrimSpace(mount.Mode))
		if mode == "" {
			mode = "ro"
		}
		if mode != "ro" && mode != "rw" {
			return nil, fmt.Errorf("mount %q mode must be ro or rw", destination)
		}
		out = append(out, sandbox.Mount{Source: source, Destination: destination, Mode: mode})
	}
	return out, nil
}

func validateContainerPath(path, label string) error {
	if path == "" || !strings.HasPrefix(path, "/") || filepath.ToSlash(filepath.Clean(path)) != path {
		return fmt.Errorf("%s %q must be an absolute, clean container path", label, path)
	}
	return nil
}

func validateEnv(env map[string]string) error {
	for name := range env {
		if !envNamePattern.MatchString(name) {
			return fmt.Errorf("invalid environment variable name %q", name)
		}
	}
	return nil
}

func appendEnv(args []string, env map[string]string) []string {
	names := make([]string, 0, len(env))
	for name := range env {
		names = append(names, name)
	}
	slicesSort(names)
	for _, name := range names {
		args = append(args, "--env", name+"="+env[name])
	}
	return args
}

func slicesSort(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}

func mountArg(mount sandbox.Mount) string {
	return "type=bind,src=" + mount.Source + ",dst=" + mount.Destination + "," + mount.Mode
}

func containerName(runID, subtaskID string) string {
	sum := sha256.Sum256([]byte(sandbox.SessionKey(runID, subtaskID)))
	return fmt.Sprintf("zenforge-%x", sum[:16])
}

func hasDestinationPrefix(path string, mounts []sandbox.Mount) bool {
	for _, mount := range mounts {
		if path == mount.Destination || strings.HasPrefix(path, mount.Destination+"/") {
			return true
		}
	}
	return false
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return 1
}

func cloneMap(input map[string]any) map[string]any {
	if input == nil {
		return nil
	}
	out := make(map[string]any, len(input))
	for key, value := range input {
		out[key] = value
	}
	return out
}

type capture struct {
	mu      sync.Mutex
	limit   int64
	written int64
	over    bool
	out     bytes.Buffer
	err     bytes.Buffer
}

type captureWriter struct {
	c   *capture
	dst *bytes.Buffer
}

func newCapture(limit int64) *capture { return &capture{limit: limit} }
func (c *capture) stdout() io.Writer  { return captureWriter{c: c, dst: &c.out} }
func (c *capture) stderr() io.Writer  { return captureWriter{c: c, dst: &c.err} }
func (w captureWriter) Write(p []byte) (int, error) {
	w.c.mu.Lock()
	defer w.c.mu.Unlock()
	allowed := int64(len(p))
	if w.c.limit > 0 && w.c.written+allowed > w.c.limit {
		allowed = w.c.limit - w.c.written
		w.c.over = true
	}
	if allowed > 0 {
		_, _ = w.dst.Write(p[:allowed])
		w.c.written += allowed
	}
	return len(p), nil
}
func (c *capture) exceeded() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.over
}
func (c *capture) strings(err error) (string, string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.out.String(), c.err.String(), err
}
