package otel

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	ztrace "github.com/feiyu912/zenforge/trace"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	oteltrace "go.opentelemetry.io/otel/trace"
)

const instrumentationName = "github.com/feiyu912/zenforge/trace/otel"

type Sink struct {
	tracer oteltrace.Tracer
}

func New(tracer oteltrace.Tracer) *Sink {
	if tracer == nil {
		tracer = otel.Tracer(instrumentationName)
	}
	return &Sink{tracer: tracer}
}

func (s *Sink) Emit(ctx context.Context, event ztrace.Event) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s == nil {
		return nil
	}
	tracer := s.tracer
	if tracer == nil {
		tracer = otel.Tracer(instrumentationName)
	}

	options := []oteltrace.SpanStartOption{oteltrace.WithAttributes(attributes(event)...)}
	if event.Timestamp > 0 {
		options = append(options, oteltrace.WithTimestamp(time.UnixMilli(event.Timestamp)))
	}
	_, span := tracer.Start(ctx, spanName(event), options...)
	if event.Timestamp > 0 {
		span.End(oteltrace.WithTimestamp(time.UnixMilli(event.Timestamp)))
		return nil
	}
	span.End()
	return nil
}

func spanName(event ztrace.Event) string {
	if event.Type == "" {
		return "zenforge.event"
	}
	return "zenforge." + event.Type
}

func attributes(event ztrace.Event) []attribute.KeyValue {
	out := []attribute.KeyValue{
		attribute.String("zenforge.event.type", event.Type),
	}
	if event.RunID != "" {
		out = append(out, attribute.String("zenforge.run_id", event.RunID))
	}
	if event.Seq > 0 {
		out = append(out, attribute.Int64("zenforge.seq", event.Seq))
	}
	if event.Timestamp > 0 {
		out = append(out, attribute.Int64("zenforge.timestamp_unix_ms", event.Timestamp))
	}
	for key, value := range event.Data {
		out = append(out, dataAttribute("zenforge.data."+key, value))
	}
	return out
}

func dataAttribute(key string, value any) attribute.KeyValue {
	switch v := value.(type) {
	case string:
		return attribute.String(key, v)
	case bool:
		return attribute.Bool(key, v)
	case int:
		return attribute.Int(key, v)
	case int64:
		return attribute.Int64(key, v)
	case float64:
		return attribute.Float64(key, v)
	case json.Number:
		if i, err := v.Int64(); err == nil {
			return attribute.Int64(key, i)
		}
		if f, err := v.Float64(); err == nil {
			return attribute.Float64(key, f)
		}
		return attribute.String(key, v.String())
	case nil:
		return attribute.String(key, "")
	default:
		data, err := json.Marshal(v)
		if err == nil {
			return attribute.String(key, string(data))
		}
		return attribute.String(key, fmt.Sprint(v))
	}
}
