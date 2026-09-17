package cli

import (
	"strings"
	"testing"

	"github.com/feiyu912/zenforge/goals"
)

func TestParseRoundReportFindsTheFinalReport(t *testing.T) {
	output := strings.Join([]string{
		"I inspected the tracer and found the missing hook.",
		"Here is some unrelated json: {\"attempt\": 1}",
		"```json",
		`{"status":"continue","summary":"wired the hook","evidence":["go test ./trace"],"nextSteps":["cover the resume path"],"blocker":""}`,
		"```",
	}, "\n")
	report, err := parseRoundReport(output)
	if err != nil {
		t.Fatalf("parseRoundReport returned error: %v", err)
	}
	if report.Status != goals.StatusContinue || report.Summary != "wired the hook" || len(report.NextSteps) != 1 {
		t.Fatalf("report = %#v", report)
	}
}

func TestParseRoundReportPrefersTheLastValidReport(t *testing.T) {
	output := `{"status":"complete","summary":"early draft","evidence":["a"],"nextSteps":[],"blocker":""}` +
		` and later ` +
		`{"status":"blocked","summary":"real end","evidence":["b"],"nextSteps":[],"blocker":"the registry is unreachable"}`
	report, err := parseRoundReport(output)
	if err != nil {
		t.Fatalf("parseRoundReport returned error: %v", err)
	}
	if report.Status != goals.StatusBlocked || report.Blocker != "the registry is unreachable" {
		t.Fatalf("report = %#v", report)
	}
}

func TestParseRoundReportRejectsMissingOrInvalidReports(t *testing.T) {
	cases := map[string]string{
		"no report":      "I did some work but never reported it.",
		"malformed json": `{"status":"continue","summary":`,
		"invalid report": `{"status":"continue","summary":"s","evidence":[],"nextSteps":[],"blocker":""}`,
		"unknown status": `{"status":"later","summary":"s","evidence":["e"],"nextSteps":[],"blocker":""}`,
	}
	for name, output := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := parseRoundReport(output); err == nil {
				t.Fatalf("output %q was accepted", output)
			}
		})
	}
}

func TestJSONObjectsIgnoresBracesInsideStrings(t *testing.T) {
	objects := jsonObjects(`prefix {"a":"}{"} suffix {"b":1}`)
	if len(objects) != 2 || objects[0] != `{"a":"}{"}` || objects[1] != `{"b":1}` {
		t.Fatalf("objects = %#v", objects)
	}
	// Braces inside a string do not end the object, so the report parses.
	report, err := parseRoundReport(`{"status":"continue","summary":"has } inside","evidence":[],"nextSteps":["a"],"blocker":""}`)
	if err != nil || report.Summary != "has } inside" {
		t.Fatalf("report = %#v err=%v", report, err)
	}
}

func TestRalphPromptCarriesOnlyThePreviousReport(t *testing.T) {
	first := ralphPrompt("ship it", 1, 3, goals.Report{})
	if !strings.Contains(first, "Previous round report: none") || !strings.Contains(first, "Objective (immutable): ship it") {
		t.Fatalf("first prompt = %q", first)
	}
	second := ralphPrompt("ship it", 2, 3, goals.Report{Status: goals.StatusContinue, Summary: "half done", NextSteps: []string{"finish"}})
	if !strings.Contains(second, `"summary":"half done"`) || !strings.Contains(second, `"nextSteps":["finish"]`) {
		t.Fatalf("second prompt = %q", second)
	}
	if strings.Contains(second, "none; this is the first round") {
		t.Fatalf("second prompt still claims to be first: %q", second)
	}
}
