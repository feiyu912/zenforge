package containerhub

import (
	"context"
	"fmt"
	"strings"

	"github.com/feiyu912/zenforge/sandbox"
)

type Adapter struct {
	client       *Client
	defaultEnvID string
}

func New(config Config) (*Adapter, error) {
	client, err := NewClient(config)
	if err != nil {
		return nil, err
	}
	return &Adapter{client: client, defaultEnvID: config.DefaultEnvID}, nil
}

func (a *Adapter) Open(ctx context.Context, req sandbox.OpenRequest) (*sandbox.Session, error) {
	req.RunID = strings.TrimSpace(req.RunID)
	if req.RunID == "" {
		return nil, fmt.Errorf("%w: run id is required", sandbox.ErrSessionOpenFailed)
	}
	if req.EnvironmentID == "" {
		req.EnvironmentID = strings.TrimSpace(a.defaultEnvID)
	}
	return a.client.CreateSession(ctx, req)
}

func (a *Adapter) Execute(ctx context.Context, session *sandbox.Session, req sandbox.ExecuteRequest) (sandbox.ExecuteResult, error) {
	if session == nil || strings.TrimSpace(session.ID) == "" {
		return sandbox.ExecuteResult{}, sandbox.ErrClosed
	}
	return a.client.ExecuteSession(ctx, session.ID, req)
}

func (a *Adapter) Close(ctx context.Context, session *sandbox.Session) error {
	if session == nil || strings.TrimSpace(session.ID) == "" {
		return sandbox.ErrClosed
	}
	return a.client.StopSession(ctx, session.ID)
}

func (a *Adapter) Prompt(ctx context.Context, environmentID string) (sandbox.Prompt, error) {
	if environmentID == "" {
		environmentID = a.defaultEnvID
	}
	return a.client.EnvironmentPrompt(ctx, environmentID)
}

func (a *Adapter) RuntimeInfo(ctx context.Context) (map[string]any, error) {
	return a.client.RuntimeInfo(ctx)
}
