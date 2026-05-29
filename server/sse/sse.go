package sse

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/feiyu912/zenforge"
)

type Options struct {
	RetryMillis int
}

func Write(w io.Writer, event zenforge.Event) error {
	if event.Seq > 0 {
		if _, err := fmt.Fprintf(w, "id: %d\n", event.Seq); err != nil {
			return err
		}
	}
	if event.Type != "" {
		if _, err := fmt.Fprintf(w, "event: %s\n", eventName(event.Type)); err != nil {
			return err
		}
	}
	data, err := json.Marshal(event.Map())
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
		return err
	}
	return nil
}

func Stream(ctx context.Context, w io.Writer, events <-chan zenforge.Event, opts Options) error {
	if opts.RetryMillis > 0 {
		if _, err := fmt.Fprintf(w, "retry: %s\n\n", strconv.Itoa(opts.RetryMillis)); err != nil {
			return err
		}
		flush(w)
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case event, ok := <-events:
			if !ok {
				return nil
			}
			if err := Write(w, event); err != nil {
				return err
			}
			flush(w)
		}
	}
}

func StreamHTTP(ctx context.Context, w http.ResponseWriter, events <-chan zenforge.Event, opts Options) error {
	headers := w.Header()
	headers.Set("Content-Type", "text/event-stream")
	headers.Set("Cache-Control", "no-cache")
	headers.Set("Connection", "keep-alive")
	headers.Set("X-Accel-Buffering", "no")
	return Stream(ctx, w, events, opts)
}

func eventName(eventType zenforge.EventType) string {
	name := strings.ReplaceAll(string(eventType), "\r", "")
	name = strings.ReplaceAll(name, "\n", "")
	return name
}

func flush(w io.Writer) {
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
}
