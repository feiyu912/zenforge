package containerhub

import (
	"context"

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
	if req.EnvironmentID == "" {
		req.EnvironmentID = a.defaultEnvID
	}
	return a.client.CreateSession(ctx, req)
}

func (a *Adapter) Execute(ctx context.Context, session *sandbox.Session, req sandbox.ExecuteRequest) (sandbox.ExecuteResult, error) {
	if session == nil {
		return sandbox.ExecuteResult{}, sandbox.ErrClosed
	}
	return a.client.ExecuteSession(ctx, session.ID, req)
}

func (a *Adapter) Close(ctx context.Context, session *sandbox.Session) error {
	if session == nil {
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
