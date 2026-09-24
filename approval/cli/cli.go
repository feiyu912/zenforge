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
	"github.com/feiyu912/zenforge/tools/askuser"
)

type Broker struct {
	In  io.Reader
	Out io.Writer

	// reader is the buffered view of In that New installs. It is created
	// once so that every prompt shares one buffer: a caller that pipes or
	// pre-writes several answers would otherwise lose all but the first,
	// because each prompt's own bufio.Reader read ahead of the next one.
	reader *bufio.Reader
}

func New(in io.Reader, out io.Writer) Broker {
	broker := Broker{In: in, Out: out}
	if in != nil {
		broker.reader = bufio.NewReader(in)
	}
	return broker
}

// bufferedReader is the reader prompts read one line from. A Broker built by
// New shares one buffer for the whole session; a Broker assembled as a struct
// literal falls back to wrapping In for that prompt.
func (b Broker) bufferedReader() *bufio.Reader {
	if b.reader != nil {
		return b.reader
	}
	return bufio.NewReader(b.In)
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
	if req.Operation == askuser.Operation {
		return b.answerQuestions(ctx, req)
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
		line, err := b.bufferedReader().ReadString('\n')
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

// answerQuestions renders an ask_user question round and collects one
// answer per question: an option number resolves to its label, anything
// else is taken as free text. The answers ride back in the decision
// payload under askuser.AnswersPayloadKey, which the tool echoes to the
// model keyed by the stable question ids.
func (b Broker) answerQuestions(ctx context.Context, req approval.Request) (approval.Decision, error) {
	questions, ok := askuser.QuestionsFromPayload(req.Payload)
	if !ok {
		return approval.Decision{}, fmt.Errorf("approval cli: user.question request carries no questions")
	}
	type response struct {
		answers map[string]any
		err     error
	}
	ch := make(chan response, 1)
	go func() {
		reader := b.bufferedReader()
		answers := make(map[string]any, len(questions))
		for _, question := range questions {
			if question.Header != "" {
				_, _ = fmt.Fprintf(b.Out, "== %s ==\n", question.Header)
			}
			_, _ = fmt.Fprintf(b.Out, "Question [%s]: %s\n", question.ID, question.Question)
			for i, option := range question.Options {
				if option.Description != "" {
					_, _ = fmt.Fprintf(b.Out, "%d. %s — %s\n", i+1, option.Label, option.Description)
					continue
				}
				_, _ = fmt.Fprintf(b.Out, "%d. %s\n", i+1, option.Label)
			}
			if question.MultiSelect {
				_, _ = fmt.Fprint(b.Out, "Select numbers (comma-separated) or type an answer> ")
			} else {
				_, _ = fmt.Fprint(b.Out, "Select a number or type an answer> ")
			}
			line, err := reader.ReadString('\n')
			if err != nil && len(line) == 0 {
				ch <- response{err: err}
				return
			}
			answer, err := resolveAnswer(strings.TrimSpace(line), question)
			if err != nil {
				ch <- response{err: err}
				return
			}
			answers[question.ID] = answer
		}
		ch <- response{answers: answers}
	}()
	select {
	case result := <-ch:
		if result.err != nil {
			return approval.Decision{}, result.err
		}
		return approval.Decision{
			RequestID: req.ID,
			Action:    approval.DecisionApprove,
			Scope:     approval.ScopeOnce,
			Payload:   map[string]any{askuser.AnswersPayloadKey: result.answers},
			DecidedAt: time.Now().UTC(),
		}, nil
	case <-ctx.Done():
		return approval.Decision{}, ctx.Err()
	}
}

func resolveAnswer(line string, question askuser.Question) (any, error) {
	if line == "" {
		return nil, fmt.Errorf("question %s needs an answer", question.ID)
	}
	if len(question.Options) == 0 {
		return line, nil
	}
	if question.MultiSelect {
		parts := strings.Split(line, ",")
		labels := make([]string, 0, len(parts))
		for _, part := range parts {
			label, err := resolveSelection(strings.TrimSpace(part), question)
			if err != nil {
				return nil, err
			}
			labels = append(labels, label)
		}
		return labels, nil
	}
	return resolveSelection(line, question)
}

// resolveSelection maps "2" to the second option label and passes any
// other non-empty text through as a free-form answer.
func resolveSelection(value string, question askuser.Question) (string, error) {
	if value == "" {
		return "", fmt.Errorf("question %s needs an answer", question.ID)
	}
	if index, err := strconv.Atoi(value); err == nil {
		if index < 1 || index > len(question.Options) {
			return "", fmt.Errorf("invalid choice %d for question %s", index, question.ID)
		}
		return question.Options[index-1].Label, nil
	}
	return value, nil
}
