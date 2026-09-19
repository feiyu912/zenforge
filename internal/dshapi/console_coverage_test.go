package dshapi

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// The console's own client declares far more remote methods than this host
// answers. docs/dsh-console-coverage.md is the honest ledger of which ones are
// served, refused by name, mounted as a stream, or absent — and this test keeps
// it from drifting: every row is checked against the routing table the host
// actually consults, so a route cannot be added or removed in code without the
// ledger moving with it (ADR 0099).

// ledgerRow matches one row of the ledger's method tables:
// `| `session/modelCatalog` | served | … |`.
var ledgerRow = regexp.MustCompile("^\\| `([a-z][a-zA-Z0-9-]*/[a-zA-Z][a-zA-Z0-9]*)` \\| (served|stream|refused|unserved) \\|")

// ledgerSummaryRow matches one row of the ledger's count table. The total row
// sets its label and count in bold.
var ledgerSummaryRow = regexp.MustCompile("^\\| (?:\\*\\*)?(served|stream|refused|unserved|client methods total)(?:\\*\\*)? \\| (?:\\*\\*)?(\\d+)(?:\\*\\*)? \\|$")

const ledgerPaths = "../../docs/dsh-console-coverage.md"

func readLedger(t *testing.T) (map[string]string, map[string]int) {
	t.Helper()
	data, err := os.ReadFile(ledgerPaths)
	if err != nil {
		t.Fatalf("read %s: %v", ledgerPaths, err)
	}
	states := map[string]string{}
	order := []string{}
	summary := map[string]int{}
	for _, line := range strings.Split(string(data), "\n") {
		if match := ledgerRow.FindStringSubmatch(line); match != nil {
			method, state := match[1], match[2]
			if previous, seen := states[method]; seen {
				t.Errorf("%s lists %q twice (%s and %s)", ledgerPaths, method, previous, state)
			}
			states[method] = state
			order = append(order, method)
			continue
		}
		if match := ledgerSummaryRow.FindStringSubmatch(line); match != nil {
			count, convErr := strconv.Atoi(match[2])
			if convErr != nil {
				t.Fatalf("ledger summary count %q: %v", match[2], convErr)
			}
			summary[match[1]] = count
		}
	}
	if len(order) < 100 {
		t.Fatalf("parsed only %d ledger rows; the ledger should list the console client's whole method surface", len(order))
	}
	if len(summary) != 5 {
		t.Fatalf("parsed %d summary rows, want 5", len(summary))
	}
	return states, summary
}

// TestConsoleCoverageLedgerMatchesTheRoutingTable is the inventory's source of
// truth: the ledger's claim about each method is compared with the host's own
// dispatcher.
func TestConsoleCoverageLedgerMatchesTheRoutingTable(t *testing.T) {
	f := newFixture(t, Config{})
	states, summary := readLedger(t)

	counts := map[string]int{}
	for method, state := range states {
		if _, routed := f.handler.method(method); routed {
			if state == "unserved" || state == "stream" {
				t.Errorf("ledger says %q is %s, but the host routes it; the ledger is stale", method, state)
			}
			continue
		}
		switch state {
		case "served", "refused":
			t.Errorf("ledger says %q is %s, but the host has no route for it; the ledger is stale", method, state)
		case "stream":
			// A logical stream on the WebSocket mux, not a unary method.
		case "unserved":
		default:
			t.Errorf("ledger gives %q the unknown state %q", method, state)
		}
		counts[state]++
	}

	for state, want := range counts {
		if got := summary[state]; got != want {
			t.Errorf("ledger's summary says %s=%d, but %d rows carry that state", state, got, want)
		}
	}
	if got, want := summary["client methods total"], len(states); got != want {
		t.Errorf("ledger's summary says the client declares %d methods, but it lists %d", got, want)
	}
	if got, want := summary["served"]+summary["stream"]+summary["refused"]+summary["unserved"], len(states); got != want {
		t.Errorf("ledger's summary counts add up to %d, but it lists %d methods", got, want)
	}
}
