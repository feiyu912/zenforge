package schedule

import (
	"strings"
	"testing"
	"time"
)

func TestParseIntervals(t *testing.T) {
	cases := map[string]time.Duration{
		"every 30s": 30 * time.Second,
		"every 5m":  5 * time.Minute,
		"every 2h":  2 * time.Hour,
		"every 5":   5 * time.Minute,
		"EVERY 1h":  time.Hour,
		"@hourly":   time.Hour,
		"@daily":    24 * time.Hour,
		"@weekly":   7 * 24 * time.Hour,
	}
	for spec, want := range cases {
		parsed, err := Parse(spec)
		if err != nil {
			t.Fatalf("Parse(%q) returned error: %v", spec, err)
		}
		if parsed.Interval != want {
			t.Fatalf("Parse(%q) interval = %s, want %s", spec, parsed.Interval, want)
		}
	}
	// @monthly is a calendar schedule, not 30 days.
	monthly, err := Parse("@monthly")
	if err != nil || monthly.Interval != 0 || len(monthly.Fields) != 5 {
		t.Fatalf("Parse(@monthly) = %#v, %v", monthly, err)
	}
	start := time.Date(2024, 1, 31, 12, 0, 0, 0, time.UTC)
	monthly.Location = time.UTC
	if next := monthly.Next(start); next.Format("2006-01-02 15:04") != "2024-02-01 00:00" {
		t.Fatalf("monthly next = %s", next)
	}
}

func TestParseRefusesBadSchedules(t *testing.T) {
	for _, spec := range []string{
		"", "   ", "every", "every 0s", "every 500ms", "every 25h", "every soon",
		"* * * *", "* * * * * *", "60 * * * *", "* 24 * * *", "* * 0 * *", "* * 32 * *",
		"* * * 13 *", "* * * * 7", "*/0 * * * *", "5-1 * * * *", "JAN * * * *", "* * * * MON",
	} {
		if _, err := Parse(spec); err == nil {
			t.Fatalf("schedule %q was accepted", spec)
		}
	}
}

func TestNextFollowsCronFields(t *testing.T) {
	location := time.UTC
	base := time.Date(2024, 3, 1, 10, 0, 0, 0, location) // Friday
	cases := []struct {
		spec string
		want string
	}{
		{"*/15 * * * *", "2024-03-01 10:15"},
		{"5 3 * * *", "2024-03-02 03:05"},
		{"0 0 1 * *", "2024-04-01 00:00"},
		{"30 9 * * 1", "2024-03-04 09:30"},
		{"0 12 1,15 * *", "2024-03-01 12:00"},
		{"0 0-6/2 * * *", "2024-03-02 00:00"},
	}
	for _, testCase := range cases {
		parsed, err := Parse(testCase.spec)
		if err != nil {
			t.Fatalf("Parse(%q) returned error: %v", testCase.spec, err)
		}
		parsed.Location = location
		next := parsed.Next(base)
		if got := next.Format("2006-01-02 15:04"); got != testCase.want {
			t.Fatalf("Next(%q) = %s, want %s", testCase.spec, got, testCase.want)
		}
	}
	// The next match is always strictly after the given instant, so a
	// scheduler that fires exactly on the minute does not fire twice.
	parsed, err := Parse("* * * * *")
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	parsed.Location = location
	exact := time.Date(2024, 3, 1, 10, 15, 0, 0, location)
	if next := parsed.Next(exact); !next.After(exact) {
		t.Fatalf("Next did not advance: %s", next)
	}
	// An impossible date terminates instead of scanning forever.
	impossible, err := Parse("0 0 31 2 *")
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	impossible.Location = location
	if next := impossible.Next(base); !next.IsZero() {
		t.Fatalf("an impossible schedule returned %s", next)
	}
	// Interval schedules step from the instant they are asked about.
	interval, err := Parse("every 10m")
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if next := interval.Next(base); next.Sub(base) != 10*time.Minute {
		t.Fatalf("interval next = %s", next)
	}
	if described := interval.Describe(); described != "every 10m0s" {
		t.Fatalf("Describe = %q", described)
	}
}

func TestParseFieldSteps(t *testing.T) {
	field, err := parseField("minute", "*/20", 0, 59)
	if err != nil {
		t.Fatalf("parseField returned error: %v", err)
	}
	if !field.Values[0] || !field.Values[20] || !field.Values[40] || field.Values[30] {
		t.Fatalf("values = %#v", field.Values)
	}
	any, err := parseField("minute", "*", 0, 59)
	if err != nil || !any.Any {
		t.Fatalf("any = %#v, %v", any, err)
	}
	if _, err := parseField("minute", "1,,2", 0, 59); err == nil {
		t.Fatal("an empty item was accepted")
	}
	if _, err := parseField("minute", "1-", 0, 59); err == nil {
		t.Fatal("a range without an end was accepted")
	}
	if _, err := parseField("minute", "", 0, 59); err == nil {
		t.Fatal("an empty field was accepted")
	}
	if strings.Contains((Spec{Raw: "0 3 * * *"}).Describe(), "every") {
		t.Fatal("a cron schedule described itself as an interval")
	}
}
