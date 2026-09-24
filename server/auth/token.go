package auth

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/feiyu912/zenforge/approval"
)

// ErrTokenNotFound reports a revoke of an id this host never minted, or one it
// has already revoked. A revoke is idempotent in effect -- the credential is
// gone either way -- but it is not silent: an operator who mistypes an id has to
// learn that nothing was removed, and a second revoke of a live credential must
// not read as success.
var ErrTokenNotFound = errors.New("token not found")

// The file's contract with its readers. The version is written so a future
// format can be refused loudly instead of misparsed, the mode is the only thing
// standing between a token file and every process on the machine, and the path
// is fixed because the store owns exactly one file.
const (
	tokenFileVersion = 1
	tokenFileMode    = 0o600
	tokenDirMode     = 0o700
	tokenTempPattern = ".tokens-*"
	// tokenIDAttempts bounds the id collision retry. A collision on 6 bytes of
	// crypto/rand is not a thing that happens; the loop exists so a store that
	// somehow holds a duplicate can still mint instead of spinning.
	tokenIDAttempts = 8
)

// tokenFile is the on-disk document: one version, one row per accepted token.
// It is decoded with unknown fields disallowed so a file written by a newer host
// is refused rather than silently narrowed.
type tokenFile struct {
	Version int     `json:"version"`
	Tokens  []Token `json:"tokens"`
}

// TokenStore is the host's token file: the credentials a deployment accepts,
// held hashed, one row per token.
//
// The plaintext of a token exists only in the return of Create. Everything the
// store writes, lists or logs is the hash, because the file is the thing an
// attacker who reaches this machine reads: hashed rows mean a stolen file cannot
// be replayed as a credential. Lookup is the only operation on the request path,
// so it takes the read lock and every mint or revoke takes the write lock --
// a host serving traffic and a console minting a token do not block each other.
type TokenStore struct {
	mu     sync.RWMutex
	path   string
	tokens []Token        // oldest minted first, the file's own order
	byHash map[string]int // hash to index in tokens, for the lookup path
}

// OpenTokenStore reads the token file at path, creating an empty one when it does
// not exist yet. An empty store refuses every token, which is the safe direction.
//
// A file another user can read is refused rather than repaired: it may already
// have been copied, and quietly tightening the mode would hide that from the
// operator who has to decide whether the tokens are burned. The mode is checked
// before the file is read for the same reason.
func OpenTokenStore(path string) (*TokenStore, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("open the token file: no path was configured")
	}
	store := &TokenStore{path: path}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.checkMode(); err != nil {
		return nil, err
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		// The file is created here, already private, so a deployment that points
		// at a path it cannot write fails at startup instead of at the first mint.
		if err := store.writeLocked(nil); err != nil {
			return nil, err
		}
	}
	if err := store.loadLocked(); err != nil {
		return nil, err
	}
	return store, nil
}

// Path is the file this store reads and writes.
func (s *TokenStore) Path() string {
	return s.path
}

// Len is the number of tokens the store holds.
func (s *TokenStore) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.tokens)
}

// List returns the stored tokens, oldest minted first. The hash is included: a
// list is an operator's view of their own host, not a public response.
//
// The returned slice is a copy, so a caller sorting or filtering it cannot
// corrupt what Lookup reads.
func (s *TokenStore) List() []Token {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Token, len(s.tokens))
	copy(out, s.tokens)
	return out
}

// Create mints one token for a tenant and subject, writes it, and returns the
// plaintext exactly once: it is never stored and cannot be read back.
//
// A token that could not be written is not added to memory either. Otherwise a
// caller would receive a secret the next Reload silently invalidates, which is
// indistinguishable from a revocation the operator did not perform.
func (s *TokenStore) Create(tenant, subject, note string) (secret string, token Token, err error) {
	if err := (approval.Namespace{Tenant: tenant, Subject: subject}).Validate(); err != nil {
		return "", Token{}, fmt.Errorf("mint a token: %w", err)
	}
	for _, field := range []struct{ name, value string }{
		{"tenant", tenant},
		{"subject", subject},
		{"note", note},
	} {
		// These values are also written to audit lines, where a newline is a
		// forged record and a control character is a terminal escape.
		if strings.IndexFunc(field.value, unicode.IsControl) >= 0 {
			return "", Token{}, fmt.Errorf(
				"mint a token: the %s may not contain control characters or newlines", field.name)
		}
	}
	secret, err = NewTokenSecret()
	if err != nil {
		return "", Token{}, fmt.Errorf("mint a token secret: %w", err)
	}

	hash := HashToken(secret)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.hasHashLocked(hash) {
		return "", Token{}, errors.New("mint a token: the new secret duplicates a stored one")
	}
	id := ""
	for attempt := 0; attempt < tokenIDAttempts; attempt++ {
		candidate := NewTokenID()
		if !s.hasIDLocked(candidate) {
			id = candidate
			break
		}
	}
	if id == "" {
		return "", Token{}, errors.New("mint a token: could not allocate an unused token id")
	}
	token = Token{
		ID:        id,
		Tenant:    tenant,
		Subject:   subject,
		Hash:      hash,
		CreatedAt: time.Now().UTC(),
		Note:      note,
	}
	next := append(append([]Token(nil), s.tokens...), token)
	if err := s.writeLocked(next); err != nil {
		return "", Token{}, err
	}
	s.setLocked(next)
	return secret, token, nil
}

// Lookup reports the token a presented secret belongs to. It is false for an
// unknown secret, an empty one, and a revoked one alike.
//
// Every row is compared without stopping at the first match, and the comparison
// itself is constant time, so how long a refusal takes says nothing about how
// close the presented value was or where in the file a real token sits. A blank
// secret is refused before hashing: a request that presented nothing is not a
// guess at a credential, and it must never match a row whose hash is the hash of
// an empty string.
func (s *TokenStore) Lookup(secret string) (Token, bool) {
	if strings.TrimSpace(secret) == "" {
		return Token{}, false
	}
	hash := HashToken(secret)
	s.mu.RLock()
	defer s.mu.RUnlock()
	match := -1
	for i, token := range s.tokens {
		if subtle.ConstantTimeCompare([]byte(token.Hash), []byte(hash)) == 1 && match < 0 {
			match = i
		}
	}
	if match < 0 {
		return Token{}, false
	}
	return s.tokens[match], true
}

// Revoke removes one token by id, which invalidates it immediately.
//
// The file is rewritten before memory is changed: an operator who revoked a
// credential has to be able to trust that the revoke survives a restart, and a
// failed write that still revoked in memory would come back to life on the next
// Reload.
func (s *TokenStore) Revoke(id string) error {
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("revoke a token in %s: no id was given: %w", s.path, ErrTokenNotFound)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	index := -1
	for i, token := range s.tokens {
		if token.ID == id {
			index = i
			break
		}
	}
	if index < 0 {
		return fmt.Errorf("revoke token %s in %s: %w", id, s.path, ErrTokenNotFound)
	}
	next := make([]Token, 0, len(s.tokens)-1)
	next = append(next, s.tokens[:index]...)
	next = append(next, s.tokens[index+1:]...)
	if err := s.writeLocked(next); err != nil {
		return err
	}
	s.setLocked(next)
	return nil
}

// Reload re-reads the file so a long-running host sees a mint or a revoke another
// process made.
//
// A file that cannot be read is an error and the loaded set is left alone: a
// host must not lose every token because a write was caught mid-flight. A file
// that no longer exists is the one exception -- it reads as an empty store, which
// refuses everything rather than honouring credentials whose file is gone.
func (s *TokenStore) Reload() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkMode(); err != nil {
		return err
	}
	return s.loadLocked()
}

// checkMode refuses a token file that anyone but its owner can reach. The refusal
// names the exact command to run, because the operator is the only one who can
// decide whether the credential is already compromised.
func (s *TokenStore) checkMode() error {
	if runtime.GOOS == "windows" {
		// Windows access control is not a POSIX mode; the bits this checks would
		// always read as permissive and refuse every file.
		return nil
	}
	info, err := os.Stat(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("inspect the token file %s: %w", s.path, err)
	}
	if info.IsDir() {
		return fmt.Errorf("the token file %s is a directory", s.path)
	}
	if mode := info.Mode().Perm(); mode&0o077 != 0 {
		return fmt.Errorf(
			"the token file %s is readable by others (mode %04o); run `chmod 600 %s`",
			s.path, mode, s.path)
	}
	return nil
}

// loadLocked replaces the in-memory set with the file's contents. The caller
// holds the write lock, and nothing changes when it returns an error.
func (s *TokenStore) loadLocked() error {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			s.setLocked(nil)
			return nil
		}
		return fmt.Errorf("read the token file %s: %w", s.path, err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		// A file truncated by a crash before the rename, or one an operator
		// emptied by hand, is an empty store and not a parse failure.
		s.setLocked(nil)
		return nil
	}
	var file tokenFile
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&file); err != nil {
		return fmt.Errorf("parse the token file %s: %w", s.path, err)
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return fmt.Errorf("parse the token file %s: unexpected data after the document", s.path)
	}
	if file.Version != tokenFileVersion {
		return fmt.Errorf(
			"the token file %s is version %d; this host reads version %d",
			s.path, file.Version, tokenFileVersion)
	}
	s.setLocked(file.Tokens)
	return nil
}

// setLocked installs a token set and rebuilds the lookup index. The caller holds
// the write lock and passes a slice it does not share.
func (s *TokenStore) setLocked(tokens []Token) {
	s.tokens = append([]Token(nil), tokens...)
	s.byHash = make(map[string]int, len(s.tokens))
	for i, token := range s.tokens {
		s.byHash[token.Hash] = i
	}
}

// hasIDLocked reports whether an id is already in the file.
func (s *TokenStore) hasIDLocked(id string) bool {
	for _, token := range s.tokens {
		if token.ID == id {
			return true
		}
	}
	return false
}

// hasHashLocked reports whether a hash is already in the file. Two rows with the
// same hash would make Lookup ambiguous, so a mint that produced one is retried.
func (s *TokenStore) hasHashLocked(hash string) bool {
	_, ok := s.byHash[hash]
	return ok
}

// writeLocked replaces the token file atomically: the document is encoded, staged
// in the same directory at `0600`, and only then renamed onto the target. A
// reader therefore sees the old file or the new one and never a half-written
// one, and a crash mid-write leaves the live file untouched.
func (s *TokenStore) writeLocked(tokens []Token) error {
	document := tokenFile{Version: tokenFileVersion, Tokens: tokens}
	if document.Tokens == nil {
		// An empty store writes `[]`, not `null`: a person reading the file
		// should see an empty list, and the next decode should not care.
		document.Tokens = []Token{}
	}
	data, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return fmt.Errorf("encode the token file %s: %w", s.path, err)
	}
	data = append(data, '\n')
	directory := filepath.Dir(s.path)
	if err := os.MkdirAll(directory, tokenDirMode); err != nil {
		return fmt.Errorf("create the directory for %s: %w", directory, err)
	}
	staging, err := os.CreateTemp(directory, tokenTempPattern)
	if err != nil {
		return fmt.Errorf("stage the token file beside %s: %w", s.path, err)
	}
	staged := staging.Name()
	// Once the rename succeeds the staged name is gone; removing it then is a
	// no-op, so this cleanup only erases a failed write's leftover.
	defer func() { _ = os.Remove(staged) }()
	if err := staging.Chmod(tokenFileMode); err != nil {
		_ = staging.Close()
		return fmt.Errorf("set the mode of the staged token file: %w", err)
	}
	if _, err := staging.Write(data); err != nil {
		_ = staging.Close()
		return fmt.Errorf("write the token file %s: %w", s.path, err)
	}
	if err := staging.Sync(); err != nil {
		_ = staging.Close()
		return fmt.Errorf("flush the token file %s: %w", s.path, err)
	}
	if err := staging.Close(); err != nil {
		return fmt.Errorf("close the staged token file: %w", err)
	}
	if err := os.Rename(staged, s.path); err != nil {
		return fmt.Errorf("replace %s: %w", s.path, err)
	}
	return nil
}
