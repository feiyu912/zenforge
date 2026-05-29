package fake

import (
	"context"
	"fmt"
	"sync"

	"github.com/feiyu912/zenforge/sandbox"
)

type Sandbox struct {
	mu sync.Mutex

	OpenCalls    []sandbox.OpenRequest
	ExecuteCalls []ExecuteCall
	CloseCalls   []sandbox.Session

	OpenError    error
	ExecuteError error
	CloseError   error
	Result       sandbox.ExecuteResult
}

type ExecuteCall struct {
	Session sandbox.Session
	Request sandbox.ExecuteRequest
}

func (s *Sandbox) Open(ctx context.Context, req sandbox.OpenRequest) (*sandbox.Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.OpenCalls = append(s.OpenCalls, req)
	if s.OpenError != nil {
		return nil, s.OpenError
	}
	return &sandbox.Session{
		ID:            sandbox.SessionKey(req.RunID, req.SubtaskID),
		RunID:         req.RunID,
		SubtaskID:     req.SubtaskID,
		EnvironmentID: req.EnvironmentID,
		WorkingDir:    req.WorkingDir,
		Metadata:      cloneMap(req.Metadata),
	}, nil
}

func (s *Sandbox) Execute(ctx context.Context, session *sandbox.Session, req sandbox.ExecuteRequest) (sandbox.ExecuteResult, error) {
	if err := ctx.Err(); err != nil {
		return sandbox.ExecuteResult{}, err
	}
	if session == nil {
		return sandbox.ExecuteResult{}, fmt.Errorf("%w: nil session", sandbox.ErrClosed)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ExecuteCalls = append(s.ExecuteCalls, ExecuteCall{Session: *session, Request: req})
	if s.ExecuteError != nil {
		return sandbox.ExecuteResult{}, s.ExecuteError
	}
	result := s.Result
	if result.WorkingDirectory == "" {
		result.WorkingDirectory = req.CWD
	}
	return result, nil
}

func (s *Sandbox) Close(ctx context.Context, session *sandbox.Session) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if session != nil {
		s.CloseCalls = append(s.CloseCalls, *session)
	}
	return s.CloseError
}

func cloneMap(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}
