package mcp

import (
	"context"
	"fmt"
	"io"
	"os/exec"
)

type StdioConfig struct {
	Command string
	Args    []string
	Env     []string
}

type StdioClient struct {
	*JSONRPCClient
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
}

func NewStdioClient(ctx context.Context, config StdioConfig) (*StdioClient, error) {
	if config.Command == "" {
		return nil, fmt.Errorf("mcp stdio command is required")
	}
	cmd := exec.CommandContext(ctx, config.Command, config.Args...)
	if len(config.Env) > 0 {
		cmd.Env = append(cmd.Environ(), config.Env...)
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
	return &StdioClient{
		JSONRPCClient: NewJSONRPCClient(stdout, stdin),
		cmd:           cmd,
		stdin:         stdin,
		stdout:        stdout,
	}, nil
}

func (c *StdioClient) Close() error {
	if c == nil {
		return nil
	}
	if c.stdin != nil {
		_ = c.stdin.Close()
	}
	if c.stdout != nil {
		_ = c.stdout.Close()
	}
	if c.cmd == nil {
		return nil
	}
	if c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
	}
	return c.cmd.Wait()
}
