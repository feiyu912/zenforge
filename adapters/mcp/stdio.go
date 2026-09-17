package mcp

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const stdioCloseGracePeriod = time.Second

// sensitiveEnvPattern matches credential-shaped environment names. It is the
// reference harness's heuristic (`KEY|PASSWORD|SECRET|TOKEN`, case
// insensitive): a name that looks like a credential is not forwarded to a
// server implicitly, so the harness's own provider key does not leak into a
// third-party process.
var sensitiveEnvPattern = regexp.MustCompile(`(?i)KEY|PASSWORD|SECRET|TOKEN`)

// BuildChildEnv returns the environment an MCP server process starts with:
// the ambient environment minus credential-shaped names, plus the server's
// explicitly configured entries. An explicit entry is never scrubbed --
// naming a credential in the server's `env` is the operator saying that
// server needs it, and the reference scrubs before merging for the same
// reason.
func BuildChildEnv(ambient []string, extra []string) []string {
	out := make([]string, 0, len(ambient)+len(extra))
	seen := make(map[string]int, len(ambient))
	for _, entry := range ambient {
		name, _, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if sensitiveEnvPattern.MatchString(name) {
			continue
		}
		if index, exists := seen[name]; exists {
			out[index] = entry
			continue
		}
		seen[name] = len(out)
		out = append(out, entry)
	}
	for _, entry := range extra {
		name, _, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if index, exists := seen[name]; exists {
			out[index] = entry
			continue
		}
		seen[name] = len(out)
		out = append(out, entry)
	}
	sort.Strings(out)
	return out
}

type StdioConfig struct {
	Command string
	Args    []string
	Env     []string
	Stderr  io.Writer
}

type StdioClient struct {
	*JSONRPCClient
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser

	ctx       context.Context
	waitDone  chan struct{}
	waitErr   error
	closeOnce sync.Once
	closeErr  error
}

func NewStdioClient(ctx context.Context, config StdioConfig) (*StdioClient, error) {
	if config.Command == "" {
		return nil, fmt.Errorf("mcp stdio command is required")
	}
	cmd := exec.CommandContext(ctx, config.Command, config.Args...)
	// The ambient environment is scrubbed rather than forwarded whole: a
	// server process is configured by the operator, but the provider
	// credentials this harness holds are not part of that configuration.
	cmd.Env = BuildChildEnv(cmd.Environ(), config.Env)
	if config.Stderr != nil {
		cmd.Stderr = config.Stderr
	} else {
		cmd.Stderr = io.Discard
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		stdin.Close()
		stdout.Close()
		return nil, err
	}
	client := &StdioClient{
		JSONRPCClient: NewJSONRPCClient(stdout, stdin),
		cmd:           cmd,
		stdin:         stdin,
		stdout:        stdout,
		ctx:           ctx,
		waitDone:      make(chan struct{}),
	}
	go func() {
		client.waitErr = cmd.Wait()
		close(client.waitDone)
	}()
	return client, nil
}

func (c *StdioClient) Close() error {
	if c == nil {
		return nil
	}
	c.closeOnce.Do(c.close)
	return c.closeErr
}

func (c *StdioClient) close() {
	c.JSONRPCClient.close()
	if c.stdin != nil {
		_ = c.stdin.Close()
	}
	if c.cmd == nil || c.waitDone == nil {
		if c.stdout != nil {
			_ = c.stdout.Close()
		}
		c.waitForReader()
		return
	}

	forced := false
	timer := time.NewTimer(stdioCloseGracePeriod)
	defer timer.Stop()
	select {
	case <-c.waitDone:
	case <-timer.C:
		if c.stdout != nil {
			_ = c.stdout.Close()
		}
		if c.cmd.Process != nil {
			if err := c.cmd.Process.Kill(); err == nil {
				forced = true
			}
		}
		<-c.waitDone
	}
	if c.stdout != nil {
		_ = c.stdout.Close()
	}
	// Closing the stream releases the reader goroutine; waiting for it keeps
	// Close's contract "when it returns, nothing is still running" true.
	c.waitForReader()

	if forced || (c.ctx != nil && c.ctx.Err() != nil) {
		return
	}
	c.closeErr = c.waitErr
}

// waitForReader waits for the reader goroutine to observe the closed stream.
func (c *StdioClient) waitForReader() {
	if c.JSONRPCClient == nil || c.readDone == nil {
		return
	}
	<-c.readDone
}
