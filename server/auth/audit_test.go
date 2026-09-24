package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// auditReadLines reads a log back the way an operator or a shipper does: it must
// end in a newline so the last record is complete, and every line must then be a
// separate string.
func auditReadLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read audit log %q returned error: %v", path, err)
	}
	if len(data) == 0 {
		return nil
	}
	if data[len(data)-1] != '\n' {
		t.Fatalf("audit log %q does not end in a newline: %q", path, data)
	}
	text := strings.TrimSuffix(string(data), "\n")
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}

func TestAuditLogWritesOrderedParseableLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")
	log, err := OpenAuditLog(path)
	if err != nil {
		t.Fatalf("OpenAuditLog returned error: %v", err)
	}
	defer log.Close()

	if got := log.Path(); got != path {
		t.Fatalf("Path: got %q want %q", got, path)
	}

	want := []Entry{
		{
			Decision:   DecisionAllow,
			Method:     http.MethodGet,
			Path:       "/run",
			Status:     200,
			Tenant:     "acme",
			Subject:    "ops",
			TokenID:    "tok_1",
			RemoteAddr: "127.0.0.1:51000",
		},
		{
			Decision:   DecisionDeny,
			Reason:     "missing-token",
			Method:     http.MethodPost,
			Path:       "/run",
			Status:     401,
			RemoteAddr: "10.0.0.9:51000",
		},
		{
			Decision:   DecisionAllowPublic,
			Method:     http.MethodGet,
			Path:       "/auth",
			Status:     200,
			RemoteAddr: "10.0.0.9:51001",
		},
	}
	for _, entry := range want {
		if err := log.Write(entry); err != nil {
			t.Fatalf("Write returned error: %v", err)
		}
	}

	lines := auditReadLines(t, path)
	if len(lines) != len(want) {
		t.Fatalf("unexpected line count: got %d want %d", len(lines), len(want))
	}
	for i, line := range lines {
		var got Entry
		if err := json.Unmarshal([]byte(line), &got); err != nil {
			t.Fatalf("line %d is not one JSON object: %v (line %q)", i, err, line)
		}
		if got.Decision != want[i].Decision || got.Path != want[i].Path || got.Status != want[i].Status {
			t.Fatalf("line %d: got decision=%q path=%q status=%d, want decision=%q path=%q status=%d",
				i, got.Decision, got.Path, got.Status, want[i].Decision, want[i].Path, want[i].Status)
		}
		if got.Time.IsZero() {
			t.Fatalf("line %d has no timestamp", i)
		}
	}
}

// TestAuditLogAppendsToExistingFileWithoutTruncating also stands in for the
// operator's workflow: a shipper may read the file while the host keeps serving,
// and a restart must not lose the record of what the host did before it.
func TestAuditLogAppendsToExistingFileWithoutTruncating(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")

	first, err := OpenAuditLog(path)
	if err != nil {
		t.Fatalf("OpenAuditLog returned error: %v", err)
	}
	if err := first.Write(Entry{Decision: DecisionAllow, Method: http.MethodGet, Path: "/first", Status: 200, RemoteAddr: "127.0.0.1:1"}); err != nil {
		t.Fatalf("Write returned error: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}
	before := auditReadLines(t, path)
	if len(before) != 1 {
		t.Fatalf("unexpected line count before reopen: got %d want 1", len(before))
	}

	second, err := OpenAuditLog(path)
	if err != nil {
		t.Fatalf("reopen returned error: %v", err)
	}
	if err := second.Write(Entry{Decision: DecisionDeny, Method: http.MethodGet, Path: "/second", Status: 401, RemoteAddr: "127.0.0.1:1"}); err != nil {
		t.Fatalf("Write after reopen returned error: %v", err)
	}
	if err := second.Close(); err != nil {
		t.Fatalf("Close after reopen returned error: %v", err)
	}

	after := auditReadLines(t, path)
	if len(after) != 2 {
		t.Fatalf("unexpected line count after reopen: got %d want 2", len(after))
	}
	if after[0] != before[0] {
		t.Fatalf("reopening changed the first record: got %q want %q", after[0], before[0])
	}
}

func TestAuditLogNilReceiverRecordsNothing(t *testing.T) {
	var log *AuditLog
	if err := log.Write(Entry{Decision: DecisionDeny, Method: http.MethodGet, Path: "/", Status: 401}); err != nil {
		t.Fatalf("nil Write returned error: %v", err)
	}
	if got := log.Path(); got != "" {
		t.Fatalf("nil Path: got %q want %q", got, "")
	}
	if err := log.Close(); err != nil {
		t.Fatalf("nil Close returned error: %v", err)
	}

	// The policy holds an AuditSink; a nil log has to be one so a host without a
	// trail does not branch at every decision.
	var sink AuditSink = log
	if err := sink.Write(Entry{Decision: DecisionAllow}); err != nil {
		t.Fatalf("nil sink Write returned error: %v", err)
	}
}

func TestAuditLogStampsZeroTimeAndPreservesExplicitTime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")
	log, err := OpenAuditLog(path)
	if err != nil {
		t.Fatalf("OpenAuditLog returned error: %v", err)
	}
	defer log.Close()

	before := time.Now().UTC()
	if err := log.Write(Entry{Decision: DecisionAllow, Method: http.MethodGet, Path: "/stamped", Status: 200}); err != nil {
		t.Fatalf("Write with zero Time returned error: %v", err)
	}
	explicit := time.Date(2024, time.March, 4, 5, 6, 7, 0, time.FixedZone("UTC+2", 2*60*60))
	if err := log.Write(Entry{Time: explicit, Decision: DecisionDeny, Method: http.MethodGet, Path: "/kept", Status: 401}); err != nil {
		t.Fatalf("Write with explicit Time returned error: %v", err)
	}
	after := time.Now().UTC()

	lines := auditReadLines(t, path)
	if len(lines) != 2 {
		t.Fatalf("unexpected line count: got %d want 2", len(lines))
	}

	var stamped Entry
	if err := json.Unmarshal([]byte(lines[0]), &stamped); err != nil {
		t.Fatalf("stamped line is not JSON: %v", err)
	}
	if stamped.Time.IsZero() {
		t.Fatalf("zero Time was not stamped")
	}
	if stamped.Time.Before(before.Add(-time.Second)) || stamped.Time.After(after.Add(time.Second)) {
		t.Fatalf("stamped time %s is outside [%s, %s]", stamped.Time, before, after)
	}
	if stamped.Time.Location() != time.UTC {
		t.Fatalf("stamped time is not UTC: got location %v", stamped.Time.Location())
	}

	var kept Entry
	if err := json.Unmarshal([]byte(lines[1]), &kept); err != nil {
		t.Fatalf("explicit line is not JSON: %v", err)
	}
	if !kept.Time.Equal(explicit) {
		t.Fatalf("explicit time was not preserved: got %s want %s", kept.Time, explicit)
	}
	if kept.Time.Format(time.RFC3339) != explicit.Format(time.RFC3339) {
		t.Fatalf("explicit time lost its offset: got %s want %s", kept.Time.Format(time.RFC3339), explicit.Format(time.RFC3339))
	}
}

// TestAuditEntryJSONKeysAreOnlyTheDeclaredFields is the guarantee that a future
// edit cannot quietly start recording a request body, a header or a credential:
// a log line carries the declared fields and nothing else.
func TestAuditEntryJSONKeysAreOnlyTheDeclaredFields(t *testing.T) {
	cases := []struct {
		name  string
		entry Entry
		want  []string
	}{
		{
			name: "allow",
			entry: Entry{
				Time:       time.Date(2024, time.March, 4, 5, 6, 7, 0, time.UTC),
				Decision:   DecisionAllow,
				Method:     http.MethodGet,
				Path:       "/run",
				Status:     200,
				Tenant:     "acme",
				Subject:    "ops",
				TokenID:    "tok_1",
				RemoteAddr: "127.0.0.1:51000",
			},
			want: []string{"time", "decision", "method", "path", "status", "tenant", "subject", "tokenId", "remoteAddr"},
		},
		{
			name: "deny",
			entry: Entry{
				Time:       time.Date(2024, time.March, 4, 5, 6, 7, 0, time.UTC),
				Decision:   DecisionDeny,
				Reason:     "missing-token",
				Method:     http.MethodPost,
				Path:       "/run",
				Status:     401,
				RemoteAddr: "10.0.0.9:51000",
			},
			want: []string{"time", "decision", "reason", "method", "path", "status", "remoteAddr"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "audit.log")
			log, err := OpenAuditLog(path)
			if err != nil {
				t.Fatalf("OpenAuditLog returned error: %v", err)
			}
			defer log.Close()
			if err := log.Write(tc.entry); err != nil {
				t.Fatalf("Write returned error: %v", err)
			}

			lines := auditReadLines(t, path)
			if len(lines) != 1 {
				t.Fatalf("unexpected line count: got %d want 1", len(lines))
			}
			var decoded map[string]any
			if err := json.Unmarshal([]byte(lines[0]), &decoded); err != nil {
				t.Fatalf("line is not a JSON object: %v", err)
			}
			got := make([]string, 0, len(decoded))
			for key := range decoded {
				got = append(got, key)
			}
			sort.Strings(got)
			want := slices.Clone(tc.want)
			sort.Strings(want)
			if !slices.Equal(got, want) {
				t.Fatalf("unexpected JSON key set: got %v want %v", got, want)
			}
		})
	}
}

func TestAuditLogConcurrentWritesProduceIntactLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")
	log, err := OpenAuditLog(path)
	if err != nil {
		t.Fatalf("OpenAuditLog returned error: %v", err)
	}

	const writers = 4
	const perWriter = 25
	var wg sync.WaitGroup
	errs := make(chan error, writers*perWriter)
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				entry := Entry{
					Decision:   DecisionAllow,
					Method:     http.MethodGet,
					Path:       fmt.Sprintf("/run/%d/%d", w, i),
					Status:     200,
					RemoteAddr: "127.0.0.1:51000",
				}
				if err := log.Write(entry); err != nil {
					errs <- err
				}
			}
		}(w)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent Write returned error: %v", err)
	}
	if err := log.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}

	lines := auditReadLines(t, path)
	if len(lines) != writers*perWriter {
		t.Fatalf("unexpected line count: got %d want %d", len(lines), writers*perWriter)
	}
	seen := make(map[string]bool, len(lines))
	for i, line := range lines {
		var got Entry
		if err := json.Unmarshal([]byte(line), &got); err != nil {
			t.Fatalf("line %d is not one intact JSON object (interleaved write?): %v (line %q)", i, err, line)
		}
		if got.Decision != DecisionAllow {
			t.Fatalf("line %d: got decision %q want %q", i, got.Decision, DecisionAllow)
		}
		if seen[got.Path] {
			t.Fatalf("line %d repeats path %q", i, got.Path)
		}
		seen[got.Path] = true
	}
}

func TestAuditLogOpenFailureNamesThePath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.log")
	if err := os.Mkdir(path, auditDirMode); err != nil {
		t.Fatalf("Mkdir returned error: %v", err)
	}

	log, err := OpenAuditLog(path)
	if err == nil {
		log.Close()
		t.Fatalf("OpenAuditLog on a directory returned nil error")
	}
	if !strings.Contains(err.Error(), path) {
		t.Fatalf("error does not name the path: %v", err)
	}

	if _, err := OpenAuditLog(""); err == nil {
		t.Fatalf("OpenAuditLog with an empty path returned nil error")
	}

	// Only the file's own directory is created. A path with an absent
	// grandparent is a configuration mistake the operator has to see, and
	// creating ancestors would give this host a tree nobody meant it to own.
	deep := filepath.Join(dir, "absent", "nested", "audit.log")
	if _, err := OpenAuditLog(deep); err == nil {
		t.Fatalf("OpenAuditLog with an absent grandparent returned nil error")
	}
	if _, statErr := os.Stat(filepath.Join(dir, "absent")); !errors.Is(statErr, fs.ErrNotExist) {
		t.Fatalf("OpenAuditLog created directories beyond the file's own: stat error %v", statErr)
	}
}

func TestAuditLogCreatedFileAndDirectoryModes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("windows does not carry POSIX file modes")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "trail", "audit.log")
	log, err := OpenAuditLog(path)
	if err != nil {
		t.Fatalf("OpenAuditLog returned error: %v", err)
	}
	defer log.Close()

	fileInfo, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat returned error: %v", err)
	}
	if got := fileInfo.Mode().Perm(); got != auditFileMode {
		t.Fatalf("unexpected file mode: got %o want %o", got, auditFileMode)
	}

	dirInfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatalf("Stat of the log directory returned error: %v", err)
	}
	if got := dirInfo.Mode().Perm(); got != auditDirMode {
		t.Fatalf("unexpected directory mode: got %o want %o", got, auditDirMode)
	}
}

func TestAuditLogWriteAfterCloseReturnsError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")
	log, err := OpenAuditLog(path)
	if err != nil {
		t.Fatalf("OpenAuditLog returned error: %v", err)
	}
	if err := log.Write(Entry{Decision: DecisionAllow, Method: http.MethodGet, Path: "/", Status: 200, RemoteAddr: "127.0.0.1:1"}); err != nil {
		t.Fatalf("Write returned error: %v", err)
	}
	if err := log.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}
	if err := log.Close(); err != nil {
		t.Fatalf("second Close returned error: %v", err)
	}

	err = log.Write(Entry{Decision: DecisionDeny, Method: http.MethodGet, Path: "/", Status: 401, RemoteAddr: "127.0.0.1:1"})
	if err == nil {
		t.Fatalf("Write after Close returned nil error")
	}
	if !strings.Contains(err.Error(), path) {
		t.Fatalf("error does not name the path: %v", err)
	}

	lines := auditReadLines(t, path)
	if len(lines) != 1 {
		t.Fatalf("Write after Close changed the file: got %d lines want 1", len(lines))
	}
}
