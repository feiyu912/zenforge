// The audit trail records identity and decisions, never payload: a line names
// who called, what this host answered, and why -- never a request body, a header
// value, or a credential. The trail is a file an operator reads, ships and keeps
// long after the request it describes, so a secret written into it outlives the
// reason it was needed, and a recorded body would put a caller's data in a
// second place nobody chose to protect. Entry is the whole schema, and the key
// set test in audit_test.go pins it so a later edit cannot widen it by accident.

package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// The modes a trail is created with. A line carries tenant, subject and token
// identity, so on a shared machine the file is readable by the operator who
// started the host and by nobody else.
const (
	auditFileMode = 0o600
	auditDirMode  = 0o700
)

// AuditLog is an append-only record of the decisions a served host made about its
// callers: one JSON object per line, written and flushed per record so a host that
// dies still says what it admitted.
//
// It is the sink the policy in this package holds once an operator has asked for
// a trail. It opens with O_APPEND and never truncates, so a restart keeps the
// record of what the host did before, and an operator -- or a log shipper -- can
// read the file while the host is still serving: a record is whole as soon as the
// Write that produced it returns. Writes from several callers are serialized, so
// two of them can never interleave halves of a line.
//
// A nil *AuditLog is a usable AuditSink that records nothing, which is how a host
// that was not asked to keep a trail runs exactly the same code path as one that
// was.
type AuditLog struct {
	path string
	// mu serializes writers inside this process. It does not coordinate two
	// processes appending to the same path: O_APPEND makes each Write land at
	// the current end of the file, so a line can follow another process's line
	// but cannot be split by it.
	mu   sync.Mutex
	file *os.File
}

// AuditLog is an AuditSink even when the pointer is nil, which is what lets the
// policy hold the interface it declares instead of a concrete type it must check.
var _ AuditSink = (*AuditLog)(nil)

// OpenAuditLog opens the log at path for appending, creating the file (0600) and
// its directory (0700) when they do not exist. It never truncates: a host that is
// restarted keeps the record of what it did before.
//
// Only the file's own directory is created, and only when it is absent: a path
// with an absent grandparent is a configuration mistake the operator has to see,
// and building the whole tree would silently give this host a directory nobody
// meant it to own. Every failure names the path and the cause, because a trail
// that quietly went nowhere is worse than a host that refused to start.
func OpenAuditLog(path string) (*AuditLog, error) {
	if path == "" {
		return nil, errors.New("auth: open audit log: path is empty")
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.Mkdir(dir, auditDirMode); err != nil && !errors.Is(err, fs.ErrExist) {
			return nil, fmt.Errorf("auth: open audit log %q: create directory %q: %w", path, dir, err)
		}
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, auditFileMode)
	if err != nil {
		return nil, fmt.Errorf("auth: open audit log %q: %w", path, err)
	}
	return &AuditLog{path: path, file: file}, nil
}

// Path is the file this log appends to. It is empty on a nil log, so a caller can
// report where a trail would be without checking the receiver first.
func (l *AuditLog) Path() string {
	if l == nil {
		return ""
	}
	return l.path
}

// Write appends one entry as a single line. A nil receiver records nothing and
// reports no error, which is what a host that was not asked to keep a trail uses.
// A zero Time is stamped with the current UTC time.
//
// The line is one Write to the open file: there is no buffer to flush and no
// second call that could be lost, so once this returns the kernel owns the whole
// record and a host that dies immediately after still leaves a readable line. A
// non-zero Time is preserved verbatim, because a caller replaying a decision
// records when the decision happened, not when it got around to logging it.
//
// A nil receiver is deliberate: the policy in this package holds an AuditSink, so
// a nil *AuditLog lets a host without a trail keep one code path instead of
// branching at every decision.
func (l *AuditLog) Write(entry Entry) error {
	if l == nil {
		return nil
	}
	if entry.Time.IsZero() {
		// UTC so lines from hosts in different zones still sort together and a
		// reader never has to guess what an offset meant.
		entry.Time = time.Now().UTC()
	}
	line, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("auth: encode audit entry: %w", err)
	}
	// One record is one line, and the newline travels with it: a reader that
	// sees a line sees a whole entry.
	line = append(line, '\n')

	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return fmt.Errorf("auth: write audit log %q: log is closed", l.path)
	}
	if _, err := l.file.Write(line); err != nil {
		return fmt.Errorf("auth: write audit log %q: %w", l.path, err)
	}
	return nil
}

// Close releases the file. It is nil-safe and idempotent, so a host that may or
// may not have opened a trail shuts down without branching and a second Close
// does nothing. A Write after Close reports an error instead of panicking, which
// keeps a request already in flight from taking the host down with it.
func (l *AuditLog) Close() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return nil
	}
	file := l.file
	l.file = nil
	if err := file.Close(); err != nil {
		return fmt.Errorf("auth: close audit log %q: %w", l.path, err)
	}
	return nil
}
