// Package schedule answers one question: when should this task run again?
// It parses a small, explicit subset of cron plus interval syntax and hands
// back a time, so the loop that runs tasks stays trivial and the parsing
// stays testable without a clock.
package schedule

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Spec is a parsed schedule.
type Spec struct {
	// Raw is what the user wrote.
	Raw string
	// Interval is set for "every 30m" style schedules.
	Interval time.Duration
	// Fields is set for cron-style schedules: minute, hour, day of month,
	// month, day of week.
	Fields []Field
	// Location is the timezone day-based fields are evaluated in.
	Location *time.Location
}

// Field is one cron field's allowed values.
type Field struct {
	// Name is the field's human name, for error messages.
	Name string
	// Min and Max bound the field.
	Min, Max int
	// Any is true when the field is "*".
	Any bool
	// Values is the explicit set of allowed values.
	Values map[int]bool
}

// Next returns the next time after the given instant. It answers
// time.Time{} when the schedule can never match again (an impossible date
// like 31 February), which the caller must treat as "stop", not "wait
// forever".
func (s Spec) Next(after time.Time) time.Time {
	if s.Interval > 0 {
		return after.Add(s.Interval)
	}
	if len(s.Fields) == 0 {
		return time.Time{}
	}
	location := s.Location
	if location == nil {
		location = time.UTC
	}
	// Walk forward a minute at a time from the next whole minute. The
	// horizon is bounded so an impossible schedule terminates instead of
	// scanning forever.
	candidate := after.In(location).Truncate(time.Minute).Add(time.Minute)
	limit := candidate.AddDate(5, 0, 0)
	for candidate.Before(limit) {
		if s.matches(candidate) {
			return candidate
		}
		candidate = candidate.Add(time.Minute)
	}
	return time.Time{}
}

func (s Spec) matches(when time.Time) bool {
	values := []int{
		when.Minute(),
		when.Hour(),
		when.Day(),
		int(when.Month()),
		int(when.Weekday()),
	}
	for index, field := range s.Fields {
		if index >= len(values) {
			break
		}
		if field.Any {
			continue
		}
		if !field.Values[values[index]] {
			return false
		}
	}
	return true
}

// maxInterval bounds an interval schedule so "every 0s" cannot become a hot
// loop.
const maxInterval = 24 * time.Hour

// Parse reads a schedule. Accepted forms:
//
//	every 30s | every 5m | every 2h        fixed interval
//	@hourly | @daily | @weekly | @monthly  named shorthands
//	*/5 * * * *                            cron subset
//
// The cron subset supports "*", numbers, ranges ("1-5"), lists ("1,15"), and
// steps ("*/10", "1-30/5"). Names (JAN, MON) are not supported: a schedule
// that silently misses because of a typo is worse than one that refuses to
// parse.
func Parse(spec string) (Spec, error) {
	raw := strings.TrimSpace(spec)
	if raw == "" {
		return Spec{}, fmt.Errorf("schedule is empty")
	}
	parsed := Spec{Raw: raw, Location: time.Local}
	lower := strings.ToLower(raw)

	if strings.HasPrefix(lower, "every ") {
		interval, err := parseDuration(strings.TrimSpace(lower[len("every "):]))
		if err != nil {
			return Spec{}, err
		}
		parsed.Interval = interval
		return parsed, nil
	}
	switch lower {
	case "@hourly":
		parsed.Interval = time.Hour
		return parsed, nil
	case "@daily", "@midnight":
		parsed.Interval = 24 * time.Hour
		return parsed, nil
	case "@weekly":
		parsed.Interval = 7 * 24 * time.Hour
		return parsed, nil
	case "@monthly":
		// A month is not a fixed duration, so it becomes a real cron
		// schedule instead of an approximation.
		return Parse("0 0 1 * *")
	}

	parts := strings.Fields(raw)
	if len(parts) != 5 {
		return Spec{}, fmt.Errorf("schedule %q must be 'every <duration>' or five cron fields (minute hour day month weekday)", spec)
	}
	names := []string{"minute", "hour", "day of month", "month", "day of week"}
	bounds := [][2]int{{0, 59}, {0, 23}, {1, 31}, {1, 12}, {0, 6}}
	for index, part := range parts {
		field, err := parseField(names[index], part, bounds[index][0], bounds[index][1])
		if err != nil {
			return Spec{}, err
		}
		parsed.Fields = append(parsed.Fields, field)
	}
	return parsed, nil
}

func parseDuration(text string) (time.Duration, error) {
	if text == "" {
		return 0, fmt.Errorf("schedule interval is empty")
	}
	// "every 5" means five minutes, matching how people write it.
	if _, err := strconv.Atoi(text); err == nil {
		text += "m"
	}
	duration, err := time.ParseDuration(text)
	if err != nil {
		return 0, fmt.Errorf("schedule interval %q is not a duration (want e.g. 30s, 5m, 2h)", text)
	}
	if duration < time.Second {
		return 0, fmt.Errorf("schedule interval %s is below the one second floor", duration)
	}
	if duration > maxInterval {
		return 0, fmt.Errorf("schedule interval %s is above the %s ceiling", duration, maxInterval)
	}
	return duration, nil
}

func parseField(name, text string, min, max int) (Field, error) {
	field := Field{Name: name, Min: min, Max: max, Values: map[int]bool{}}
	if text == "*" {
		field.Any = true
		return field, nil
	}
	for _, item := range strings.Split(text, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			return Field{}, fmt.Errorf("%s field %q has an empty item", name, text)
		}
		step := 1
		if base, stepText, ok := strings.Cut(item, "/"); ok {
			parsedStep, err := strconv.Atoi(stepText)
			if err != nil || parsedStep <= 0 {
				return Field{}, fmt.Errorf("%s field step %q is not a positive number", name, stepText)
			}
			step = parsedStep
			item = strings.TrimSpace(base)
		}
		start, end := 0, 0
		switch {
		case item == "*":
			start, end = min, max
		case strings.Contains(item, "-"):
			left, right, _ := strings.Cut(item, "-")
			parsedStart, err := strconv.Atoi(strings.TrimSpace(left))
			if err != nil {
				return Field{}, fmt.Errorf("%s field %q is not a number or range", name, item)
			}
			parsedEnd, err := strconv.Atoi(strings.TrimSpace(right))
			if err != nil {
				return Field{}, fmt.Errorf("%s field %q has a bad range end", name, item)
			}
			start, end = parsedStart, parsedEnd
		default:
			parsed, err := strconv.Atoi(item)
			if err != nil {
				return Field{}, fmt.Errorf("%s field %q is not a number, range, or *", name, item)
			}
			start, end = parsed, parsed
		}
		if start < min || end > max {
			return Field{}, fmt.Errorf("%s field %q is outside %d-%d", name, item, min, max)
		}
		if start > end {
			return Field{}, fmt.Errorf("%s field %q has a range that runs backwards", name, item)
		}
		for value := start; value <= end; value += step {
			field.Values[value] = true
		}
	}
	if len(field.Values) == 0 {
		return Field{}, fmt.Errorf("%s field %q matches nothing", name, text)
	}
	return field, nil
}

// Describe renders a schedule for logs.
func (s Spec) Describe() string {
	if s.Interval > 0 {
		return "every " + s.Interval.String()
	}
	return strings.Join(strings.Fields(s.Raw), " ")
}
