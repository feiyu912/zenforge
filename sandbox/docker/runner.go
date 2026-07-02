package docker

import (
	"context"
	"io"
	"os/exec"
)

type execRunner struct{}

func (execRunner) Run(ctx context.Context, executable string, args []string, stdout, stderr io.Writer) error {
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}
