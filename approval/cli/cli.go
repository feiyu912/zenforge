package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/feiyu912/zenforge/approval"
)

type Broker struct {
	In  io.Reader
	Out io.Writer
}

func New(in io.Reader, out io.Writer) Broker {
	return Broker{In: in, Out: out}
}

func (b Broker) Request(ctx context.Context, req approval.Request) (approval.Decision, error) {
	if err := ctx.Err(); err != nil {
		return approval.Decision{}, err
	}
	if err := req.Validate(); err != nil {
		return approval.Decision{}, err
	}
	if b.In == nil {
		return approval.Decision{}, fmt.Errorf("approval cli input is not configured")
	}
	if b.Out == nil {
		b.Out = io.Discard
	}
	_, _ = fmt.Fprintf(b.Out, "Approval required: %s\n", req.Title)
	if req.Description != "" {
		_, _ = fmt.Fprintf(b.Out, "%s\n", req.Description)
	}
	_, _ = fmt.Fprintf(b.Out, "Risk: %s\n", req.Risk)
	for i, option := range req.Options {
		label := option.Label
		if label == "" {
			label = string(option.Action)
		}
		_, _ = fmt.Fprintf(b.Out, "%d. %s\n", i+1, label)
	}
	_, _ = fmt.Fprint(b.Out, "> ")

	type response struct {
		decision approval.Decision
		err      error
	}
	ch := make(chan response, 1)
	go func() {
		line, err := bufio.NewReader(b.In).ReadString('\n')
		if err != nil && len(line) == 0 {
			ch <- response{err: err}
			return
		}
		index, err := strconv.Atoi(strings.TrimSpace(line))
		if err != nil || index < 1 || index > len(req.Options) {
			ch <- response{err: fmt.Errorf("invalid approval choice")}
			return
		}
		option := req.Options[index-1]
		ch <- response{decision: approval.Decision{
			RequestID: req.ID,
			Action:    option.Action,
			Scope:     option.Scope,
			DecidedAt: time.Now().UTC(),
		}}
	}()
	select {
	case result := <-ch:
		return result.decision, result.err
	case <-ctx.Done():
		return approval.Decision{}, ctx.Err()
	}
}
