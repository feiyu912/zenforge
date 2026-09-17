package tool

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode/utf8"
)

// Spill defaults follow the DSH spill store: only ~50 KiB of a tool
// result stays inline, a head/tail preview marks the cut, and the full
// output lives in a private on-disk file the model can read back with
// the workspace tools when the pointer is inside its read roots.
const (
	DefaultSpillMaxInlineBytes = 50 * 1024
	DefaultSpillHeadBytes      = 4 * 1024
	DefaultSpillTailBytes      = 1 * 1024
	spillDirMode               = 0o700
	spillFileMode              = 0o600
)

// SpillStore persists oversized payloads in a private on-disk store,
// mirroring the DSH spill-store seam: the store owns directory creation
// (0700), file permissions (0600), and name sanitization, while callers
// own naming and formatting. It is safe for concurrent use and idempotent
// per suggested name — a retried call overwrites its own file.
type SpillStore struct {
	dir      string
	once     sync.Once
	resolved string
	err      error
}

// NewSpillStore returns a store writing under dir. An empty dir selects
// a private per-process directory under os.TempDir() on first save.
func NewSpillStore(dir string) *SpillStore {
	return &SpillStore{dir: dir}
}

// SaveText writes content to a sanitized file derived from suggestedName
// and returns its absolute path.
func (s *SpillStore) SaveText(suggestedName, content string) (string, error) {
	if s == nil {
		return "", fmt.Errorf("spill store is nil")
	}
	dir, err := s.ensureDir()
	if err != nil {
		return "", err
	}
	name := sanitizePathComponent(suggestedName)
	if name == "" || name == "unscoped" {
		name = "spill"
	}
	if filepath.Ext(name) == "" {
		name += ".txt"
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), spillFileMode); err != nil {
		return "", err
	}
	return path, nil
}

func (s *SpillStore) ensureDir() (string, error) {
	s.once.Do(func() {
		target := s.dir
		if target == "" {
			target, s.err = os.MkdirTemp("", "zenforge-spill-")
			if s.err != nil {
				return
			}
			if chmodErr := os.Chmod(target, spillDirMode); chmodErr != nil {
				s.err = chmodErr
				return
			}
			s.resolved = target
			return
		}
		if s.err = os.MkdirAll(target, spillDirMode); s.err != nil {
			return
		}
		s.resolved = target
	})
	return s.resolved, s.err
}

// SpillConfig configures the Spill middleware.
type SpillConfig struct {
	// MaxInlineBytes is the largest result output kept inline. Zero or
	// negative selects DefaultSpillMaxInlineBytes.
	MaxInlineBytes int
	// HeadBytes and TailBytes size the preview kept inline. Zero or
	// negative selects the defaults; both are clamped below
	// MaxInlineBytes.
	HeadBytes int
	TailBytes int
	// Dir is the spill store directory, created 0700 on first spill.
	// Empty selects a private per-process directory under os.TempDir().
	// Point it inside the workspace (for example .zenforge/spill) so the
	// model can read spilled files with the workspace read tool.
	Dir string
	// Store overrides Dir with a caller-provided store, letting tools
	// and the middleware share one spill location.
	Store *SpillStore
}

// Spill returns a Middleware that moves oversized tool output to a
// private on-disk store, keeping a head/tail preview plus a pointer
// inline. Spill failures are fail-soft: the output is then truncated
// inline like MaxOutputBytes so an oversized result never reaches the
// model context unchecked.
func Spill(config SpillConfig) Middleware {
	maxInline := config.MaxInlineBytes
	if maxInline <= 0 {
		maxInline = DefaultSpillMaxInlineBytes
	}
	head := config.HeadBytes
	if head <= 0 {
		head = DefaultSpillHeadBytes
	}
	tail := config.TailBytes
	if tail <= 0 {
		tail = DefaultSpillTailBytes
	}
	if head > maxInline/2 {
		head = maxInline / 2
	}
	if tail > maxInline/4 {
		tail = maxInline / 4
	}
	store := config.Store
	if store == nil {
		store = NewSpillStore(config.Dir)
	}
	return func(next Invoker) Invoker {
		return InvokerFunc(func(ctx context.Context, call Call) (Result, error) {
			result, err := next.Invoke(ctx, call)
			if len(result.Output) <= maxInline {
				return result, err
			}
			originalBytes := len(result.Output)
			sum := sha256.Sum256([]byte(strings.TrimSpace(string(call.Arguments))))
			name := fmt.Sprintf("%s-%s-%s.txt",
				sanitizePathComponent(call.RunID),
				sanitizePathComponent(call.ID),
				hex.EncodeToString(sum[:8]))
			path, writeErr := store.SaveText(name, result.Output)
			if writeErr != nil {
				// Fail soft: bounded inline truncation beats an
				// unbounded result or a failed call.
				result.Output = truncateUTF8(result.Output, head) +
					fmt.Sprintf("\n\n[output spilled: %d bytes; spill store unavailable (%s); output truncated]\n", originalBytes, truncateUTF8(writeErr.Error(), 160))
				if result.Metadata == nil {
					result.Metadata = map[string]any{}
				}
				result.Metadata["spilled"] = false
				result.Metadata["originalBytes"] = originalBytes
				return result, err
			}
			preview := truncateUTF8(result.Output, head) +
				fmt.Sprintf("\n\n[output spilled: full %d bytes written to %s — read the file for the omitted middle]\n\n", originalBytes, path) +
				tailUTF8(result.Output, tail)
			result.Output = preview
			if result.Metadata == nil {
				result.Metadata = map[string]any{}
			}
			result.Metadata["spilled"] = true
			result.Metadata["spillPath"] = path
			result.Metadata["originalBytes"] = originalBytes
			return result, err
		})
	}
}

func sanitizePathComponent(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "unscoped"
	}
	var builder strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-', r == '.':
			builder.WriteRune(r)
		default:
			builder.WriteRune('_')
		}
	}
	return builder.String()
}

// tailUTF8 returns the last max bytes of value without splitting a rune.
func tailUTF8(value string, max int) string {
	if max <= 0 || len(value) <= max {
		return value
	}
	out := value[len(value)-max:]
	for len(out) > 0 && !utf8.ValidString(out) {
		out = out[1:]
	}
	return out
}
