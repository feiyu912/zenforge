package mcp

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"
)

const stdioCloseGracePeriod = time.Second

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
	if len(config.Env) > 0 {
		cmd.Env = append(cmd.Environ(), config.Env...)
	}
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

	if forced || (c.ctx != nil && c.ctx.Err() != nil) {
		return
	}
	c.closeErr = c.waitErr
}
