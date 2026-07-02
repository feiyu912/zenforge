// Package docker provides a Docker CLI backed sandbox.
package docker

import (
	"context"
	"io"
	"time"
)

const (
	defaultImage          = "alpine:3.20"
	defaultWorkingDir     = "/workspace"
	defaultTimeout        = 30 * time.Second
	defaultMaxOutputBytes = int64(8 << 20)
)

// Runner executes the configured Docker CLI. It is injectable so callers can
// test configuration and lifecycle behavior without a Docker daemon.
type Runner interface {
	Run(ctx context.Context, executable string, args []string, stdout, stderr io.Writer) error
}

type Config struct {
	// DockerCLI is the executable name or path. It defaults to "docker".
	DockerCLI string
	// DefaultImage is used when OpenRequest.EnvironmentID is empty.
	DefaultImage      string
	DefaultWorkingDir string
	DefaultTimeout    time.Duration
	MaxOutputBytes    int64
	// NetworkMode defaults to "none". Set it explicitly to another Docker
	// network mode when containers require network access.
	NetworkMode string
	// WritableRootFS disables the default read-only container root filesystem.
	WritableRootFS bool
	PidsLimit      int
	Runner         Runner
}
