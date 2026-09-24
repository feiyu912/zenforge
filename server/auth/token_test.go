package auth

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// openStore opens a store over a path inside the test's own directory, which is
// removed with the test. Real files are the point: the store's contract is about
// bytes on disk, not about an in-memory map.
func openStore(t *testing.T, name string) (*TokenStore, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	store, err := OpenTokenStore(path)
	if err != nil {
		t.Fatalf("OpenTokenStore(%q): %v", path, err)
	}
	return store, path
}

// mint mints one token and fails the test if the store refused it.
func mint(t *testing.T, store *TokenStore, tenant, subject, note string) (string, Token) {
	t.Helper()
	secret, token, err := store.Create(tenant, subject, note)
	if err != nil {
		t.Fatalf("Create(%q, %q, %q): %v", tenant, subject, note, err)
	}
	return secret, token
}

func TestTokenStoreMintAndLookupRoundTrip(t *testing.T) {
	store, _ := openStore(t, "tokens.json")
	if got := store.Len(); got != 0 {
		t.Fatalf("a fresh store holds %d tokens, want 0", got)
	}
	if _, ok := store.Lookup("zf_nothing_like_this"); ok {
		t.Fatal("a fresh store resolved a secret it never minted")
	}

	secret, token := mint(t, store, "acme", "ci", "nightly build")
	if token.ID == "" {
		t.Fatal("the minted token has no id")
	}
	if token.Hash != HashToken(secret) {
		t.Fatalf("stored hash %q does not hash the returned secret", token.Hash)
	}
	if token.Tenant != "acme" || token.Subject != "ci" || token.Note != "nightly build" {
		t.Fatalf("minted token lost its fields: %+v", token)
	}
	if token.CreatedAt.IsZero() || token.CreatedAt.Location() != time.UTC {
		t.Fatalf("CreatedAt = %v, want a UTC timestamp", token.CreatedAt)
	}

	found, ok := store.Lookup(secret)
	if !ok {
		t.Fatal("Lookup did not resolve the secret that was just minted")
	}
	if found.ID != token.ID {
		t.Fatalf("Lookup resolved token %q, want %q", found.ID, token.ID)
	}
	namespace := found.Namespace()
	if namespace.Tenant != "acme" || namespace.Subject != "ci" {
		t.Fatalf("Lookup resolved the wrong namespace: %+v", namespace)
	}
	if _, ok := store.Lookup(secret + "x"); ok {
		t.Fatal("Lookup resolved a secret with a trailing byte appended")
	}
	// Lookup is the identity seam's only call, so it must satisfy TokenSource.
	var _ TokenSource = store
}

func TestTokenStoreReopenPersists(t *testing.T) {
	store, path := openStore(t, "tokens.json")
	secret, token := mint(t, store, "acme", "ci", "kept")

	reopened, err := OpenTokenStore(path)
	if err != nil {
		t.Fatalf("reopen %q: %v", path, err)
	}
	if reopened.Len() != 1 {
		t.Fatalf("reopened store holds %d tokens, want 1", reopened.Len())
	}
	found, ok := reopened.Lookup(secret)
	if !ok || found.ID != token.ID {
		t.Fatalf("reopened store resolved (%+v, %v), want %q", found, ok, token.ID)
	}
	if len(reopened.List()) != 1 || reopened.List()[0].Hash != token.Hash {
		t.Fatalf("reopened List() = %+v, want the stored hash", reopened.List())
	}
}

func TestTokenStoreSecretIsNeverOnDisk(t *testing.T) {
	store, path := openStore(t, "tokens.json")
	first, firstToken := mint(t, store, "acme", "ci", "first")
	second, _ := mint(t, store, "globex", "ops", "second")

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %q: %v", path, err)
	}
	for _, secret := range []string{first, second} {
		if bytes.Contains(data, []byte(secret)) {
			t.Fatalf("the token file contains the plaintext secret %q", secret)
		}
	}
	if !bytes.Contains(data, []byte(firstToken.Hash)) {
		t.Fatalf("the token file does not contain the hash %q", firstToken.Hash)
	}
	if !bytes.Contains(data, []byte("sha256:")) {
		t.Fatal("the token file has no sha256: hash row")
	}
	// List is an operator's view, not a public response, but even there the
	// plaintext never appears -- only the hash.
	for _, listed := range store.List() {
		if listed.Hash == first || listed.Hash == second {
			t.Fatalf("List() returned a plaintext secret in the hash field: %+v", listed)
		}
	}
	if listed := store.List()[0]; listed.ID != firstToken.ID {
		t.Fatalf("List()[0].ID = %q, want %q", listed.ID, firstToken.ID)
	}
}

func TestTokenStoreLookupHashesUseConstantTimeCompare(t *testing.T) {
	// The comparison itself is not observable from the outside, so the line is
	// pinned at the source: a byte-wise == or a map probe on the hash would
	// still pass every behavioural test and leak through timing.
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller could not locate the test source")
	}
	source, err := os.ReadFile(filepath.Join(filepath.Dir(sourceFile), "token.go"))
	if err != nil {
		t.Fatalf("read token.go: %v", err)
	}
	if !bytes.Contains(source, []byte("subtle.ConstantTimeCompare")) {
		t.Fatal("token.go does not compare hashes with subtle.ConstantTimeCompare")
	}

	store, path := openStore(t, "tokens.json")
	// A row whose hash is the hash of the empty string is the trap: a Lookup
	// that hashes first and compares would hand out a credential for a blank
	// bearer token, which is what an unauthenticated client sends.
	writeTokenFile(t, path, []map[string]any{{
		"id":        "tok_blank",
		"tenant":    "acme",
		"subject":   "ci",
		"hash":      HashToken(""),
		"createdAt": "2024-01-01T00:00:00Z",
		"note":      "",
	}})
	if err := store.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	for _, secret := range []string{"", " ", "\t", "\n", "  \t\n "} {
		if found, ok := store.Lookup(secret); ok {
			t.Fatalf("Lookup(%q) resolved %+v, want a refusal for a blank secret", secret, found)
		}
	}
	// A real secret still resolves, and its whitespace-trimmed form does not:
	// the check is on the presented value, not on some normalised variant.
	stock, stockToken := mint(t, store, "globex", "ops", "real")
	if found, ok := store.Lookup(stock); !ok || found.ID != stockToken.ID {
		t.Fatalf("Lookup of a real secret = (%+v, %v), want %q", found, ok, stockToken.ID)
	}
	if _, ok := store.Lookup(" " + stock + " "); ok {
		t.Fatal("Lookup resolved a secret padded with whitespace")
	}
}

func TestTokenStoreRevokeInvalidatesNowAndAfterReload(t *testing.T) {
	store, path := openStore(t, "tokens.json")
	secret, token := mint(t, store, "acme", "ci", "to be revoked")
	if store.Len() != 1 {
		t.Fatalf("store holds %d tokens before revoke, want 1", store.Len())
	}
	if err := store.Revoke(token.ID); err != nil {
		t.Fatalf("Revoke(%q): %v", token.ID, err)
	}
	if _, ok := store.Lookup(secret); ok {
		t.Fatal("a revoked secret still resolves")
	}
	if store.Len() != 0 {
		t.Fatalf("store holds %d tokens after revoke, want 0", store.Len())
	}
	if err := store.Reload(); err != nil {
		t.Fatalf("Reload after revoke: %v", err)
	}
	if _, ok := store.Lookup(secret); ok {
		t.Fatal("a revoked secret resolves again after Reload")
	}
	if store.Len() != 0 {
		t.Fatalf("store holds %d tokens after Reload, want 0", store.Len())
	}
	// The revoke has to be on disk, not only in memory: a restart must not
	// resurrect the credential.
	reopened, err := OpenTokenStore(path)
	if err != nil {
		t.Fatalf("reopen %q: %v", path, err)
	}
	if _, ok := reopened.Lookup(secret); ok {
		t.Fatal("a revoked secret resolves after the store was reopened")
	}
	if err := reopened.Revoke(token.ID); !errors.Is(err, ErrTokenNotFound) {
		t.Fatalf("revoking an already revoked id = %v, want ErrTokenNotFound", err)
	}
}

func TestTokenStoreRevokeUnknownAndEmptyID(t *testing.T) {
	store, _ := openStore(t, "tokens.json")
	mint(t, store, "acme", "ci", "still here")

	for _, id := range []string{"tok_missing", "", "   ", "tok_000000000000"} {
		err := store.Revoke(id)
		if !errors.Is(err, ErrTokenNotFound) {
			t.Fatalf("Revoke(%q) = %v, want an error matching ErrTokenNotFound", id, err)
		}
	}
	// A refused revoke changes nothing: a real token is not collateral damage.
	if store.Len() != 1 {
		t.Fatalf("store holds %d tokens after refused revokes, want 1", store.Len())
	}
}

func TestTokenStoreReloadReplacesTheLoadedSet(t *testing.T) {
	store, path := openStore(t, "tokens.json")
	firstSecret, firstToken := mint(t, store, "acme", "ci", "first")

	// Another process is a second store over the same file: it mints one token
	// and revokes the one this store loaded.
	other, err := OpenTokenStore(path)
	if err != nil {
		t.Fatalf("open a second store over %q: %v", path, err)
	}
	secondSecret, secondToken := mint(t, other, "globex", "ops", "second")
	if err := other.Revoke(firstToken.ID); err != nil {
		t.Fatalf("revoke through the second store: %v", err)
	}

	if _, ok := store.Lookup(secondSecret); ok {
		t.Fatal("this store saw a mint it has not reloaded")
	}
	if err := store.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	found, ok := store.Lookup(secondSecret)
	if !ok || found.ID != secondToken.ID {
		t.Fatalf("after Reload Lookup = (%+v, %v), want %q", found, ok, secondToken.ID)
	}
	if _, ok := store.Lookup(firstSecret); ok {
		t.Fatal("after Reload this store still holds a token another process revoked")
	}
	if store.Len() != 1 {
		t.Fatalf("after Reload the store holds %d tokens, want 1", store.Len())
	}
}

func TestTokenStoreReloadFailureKeepsTheLoadedSet(t *testing.T) {
	store, path := openStore(t, "tokens.json")
	secret, token := mint(t, store, "acme", "ci", "survives")

	cases := []struct {
		name    string
		content string
	}{
		{"malformed JSON", `{"version":1,"tokens":[`},
		{"truncated write", `{"version":1,"tokens":[{"id":"tok_a"`},
		{"wrong version", `{"version":2,"tokens":[]}`},
		{"unknown field", `{"version":1,"tokens":[],"extra":true}`},
		{"trailing document", `{"version":1,"tokens":[]}{"version":1,"tokens":[]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(tc.content), tokenFileMode); err != nil {
				t.Fatalf("write %q: %v", path, err)
			}
			if err := store.Reload(); err == nil {
				t.Fatal("Reload accepted a file it should have refused")
			}
			// A host must not lose its tokens because of a bad write.
			found, ok := store.Lookup(secret)
			if !ok || found.ID != token.ID {
				t.Fatalf("after a failed Reload Lookup = (%+v, %v), want %q", found, ok, token.ID)
			}
			if store.Len() != 1 {
				t.Fatalf("after a failed Reload the store holds %d tokens, want 1", store.Len())
			}
		})
	}
}

func TestTokenStoreRefusesBadDocuments(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    []string
	}{
		{
			name:    "version 2",
			content: `{"version":2,"tokens":[]}`,
			want:    []string{"version 2", "version 1"},
		},
		{
			name:    "version 0",
			content: `{"version":0,"tokens":[]}`,
			want:    []string{"version 0"},
		},
		{
			name:    "unknown top-level field",
			content: `{"version":1,"tokens":[],"secrets":[]}`,
			want:    []string{"secrets"},
		},
		{
			name: "unknown token field",
			content: `{"version":1,"tokens":[{"id":"tok_a","tenant":"acme",` +
				`"subject":"ci","hash":"sha256:00","createdAt":"2024-01-01T00:00:00Z",` +
				`"plaintext":"zf_leaked"}]}`,
			want: []string{"plaintext"},
		},
		{
			name:    "malformed JSON",
			content: `{"version":1,`,
			want:    nil,
		},
		{
			name:    "not JSON at all",
			content: "this is not a token file\n",
			want:    nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "tokens.json")
			if err := os.WriteFile(path, []byte(tc.content), tokenFileMode); err != nil {
				t.Fatalf("write %q: %v", path, err)
			}
			store, err := OpenTokenStore(path)
			if err == nil {
				t.Fatalf("OpenTokenStore accepted %s and returned %+v", tc.name, store)
			}
			if !strings.Contains(err.Error(), path) {
				t.Fatalf("the error does not name the file: %v", err)
			}
			for _, want := range tc.want {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("the error %q does not mention %q", err, want)
				}
			}
		})
	}
}

func TestTokenStoreEmptyAndMissingFilesAreEmptyStores(t *testing.T) {
	cases := []struct {
		name    string
		content string
		write   bool
	}{
		{name: "missing file", write: false},
		{name: "zero-byte file", content: "", write: true},
		{name: "whitespace only", content: "\n\t \n", write: true},
		{name: "empty token list", content: `{"version":1,"tokens":[]}`, write: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "tokens.json")
			if tc.write {
				if err := os.WriteFile(path, []byte(tc.content), tokenFileMode); err != nil {
					t.Fatalf("write %q: %v", path, err)
				}
			}
			store, err := OpenTokenStore(path)
			if err != nil {
				t.Fatalf("OpenTokenStore(%q): %v", path, err)
			}
			if store.Path() != path {
				t.Fatalf("Path() = %q, want %q", store.Path(), path)
			}
			if store.Len() != 0 {
				t.Fatalf("the store holds %d tokens, want 0", store.Len())
			}
			// An empty store refuses every token, which is the safe direction.
			for _, secret := range []string{"", "zf_anything", HashToken("zf_anything")} {
				if found, ok := store.Lookup(secret); ok {
					t.Fatalf("an empty store resolved %q to %+v", secret, found)
				}
			}
			if err := store.Reload(); err != nil {
				t.Fatalf("Reload on %s: %v", tc.name, err)
			}
			if store.Len() != 0 {
				t.Fatalf("after Reload the store holds %d tokens, want 0", store.Len())
			}
		})
	}
}

func TestTokenStoreReopensAnEmptyFileItCreated(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "deeper", "tokens.json")
	store, err := OpenTokenStore(path)
	if err != nil {
		t.Fatalf("OpenTokenStore(%q): %v", path, err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the store did not create %q: %v", path, err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %q: %v", path, err)
		}
		if mode := info.Mode().Perm(); mode != tokenFileMode {
			t.Fatalf("the created file has mode %04o, want %04o", mode, tokenFileMode)
		}
	}
	if store.Len() != 0 {
		t.Fatalf("the created store holds %d tokens, want 0", store.Len())
	}
}

func TestTokenStoreRefusesWorldReadableFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX mode bits do not mean anything on Windows")
	}
	path := filepath.Join(t.TempDir(), "tokens.json")
	store, err := OpenTokenStore(path)
	if err != nil {
		t.Fatalf("OpenTokenStore(%q): %v", path, err)
	}
	secret, _ := mint(t, store, "acme", "ci", "exposed")

	for _, mode := range []os.FileMode{0o644, 0o640, 0o604, 0o660, 0o606, 0o666} {
		if err := os.Chmod(path, mode); err != nil {
			t.Fatalf("chmod %q to %04o: %v", path, mode, err)
		}
		opened, err := OpenTokenStore(path)
		if err == nil {
			t.Fatalf("OpenTokenStore accepted a file with mode %04o: %+v", mode, opened)
		}
		if !strings.Contains(err.Error(), "chmod 600 "+path) {
			t.Fatalf("the error does not tell the operator to chmod: %v", err)
		}
		if !strings.Contains(err.Error(), path) {
			t.Fatalf("the error does not name the file: %v", err)
		}
		// A refused reload must not silently drop a credentialed store.
		if err := store.Reload(); err == nil {
			t.Fatalf("Reload accepted a file with mode %04o", mode)
		}
		if _, ok := store.Lookup(secret); !ok {
			t.Fatalf("a refused Reload at mode %04o dropped the loaded tokens", mode)
		}
	}
	if err := os.Chmod(path, tokenFileMode); err != nil {
		t.Fatalf("restore the mode of %q: %v", path, err)
	}
	if _, err := OpenTokenStore(path); err != nil {
		t.Fatalf("a 0600 file was refused: %v", err)
	}
}

func TestTokenStoreRefusesControlCharacters(t *testing.T) {
	cases := []struct {
		name    string
		tenant  string
		subject string
		note    string
		field   string
	}{
		{name: "newline in tenant", tenant: "ac\nme", subject: "ci", field: "tenant"},
		{name: "carriage return in tenant", tenant: "ac\rme", subject: "ci", field: "tenant"},
		{name: "tab in subject", tenant: "acme", subject: "c\ti", field: "subject"},
		{name: "nul in subject", tenant: "acme", subject: "ci\x00", field: "subject"},
		{
			name: "escape in note", tenant: "acme", subject: "ci",
			note: "log\x1b[31m", field: "note",
		},
		{name: "newline in note", tenant: "acme", subject: "ci", note: "a\nb", field: "note"},
		{name: "delete in note", tenant: "acme", subject: "ci", note: "a\x7fb", field: "note"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, path := openStore(t, "tokens.json")
			_, _, err := store.Create(tc.tenant, tc.subject, tc.note)
			if err == nil {
				t.Fatalf("Create(%q, %q, %q) was accepted", tc.tenant, tc.subject, tc.note)
			}
			if !strings.Contains(err.Error(), tc.field) {
				t.Fatalf("the error %q does not name the %s field", err, tc.field)
			}
			if store.Len() != 0 {
				t.Fatalf("a refused mint added %d tokens", store.Len())
			}
			// The refusal must not have touched the file either.
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read %q: %v", path, err)
			}
			var file tokenFile
			if err := json.Unmarshal(data, &file); err != nil {
				t.Fatalf("the token file is not decodable after a refused mint: %v", err)
			}
			if len(file.Tokens) != 0 {
				t.Fatalf("a refused mint wrote %d tokens to disk", len(file.Tokens))
			}
		})
	}
}

func TestTokenStoreRefusesEmptyTenantOrSubject(t *testing.T) {
	cases := []struct {
		name    string
		tenant  string
		subject string
	}{
		{name: "empty tenant", tenant: "", subject: "ci"},
		{name: "blank tenant", tenant: "   ", subject: "ci"},
		{name: "empty subject", tenant: "acme", subject: ""},
		{name: "blank subject", tenant: "acme", subject: "\t"},
		{name: "both empty", tenant: "", subject: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, path := openStore(t, "tokens.json")
			secret, token, err := store.Create(tc.tenant, tc.subject, "note")
			if err == nil {
				t.Fatalf("Create(%q, %q) was accepted as %q / %+v",
					tc.tenant, tc.subject, secret, token)
			}
			if secret != "" {
				t.Fatalf("a refused mint returned the secret %q", secret)
			}
			if store.Len() != 0 {
				t.Fatalf("a refused mint added %d tokens", store.Len())
			}
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("stat %q: %v", path, err)
			}
		})
	}
}

func TestTokenStoreConcurrentCreateAndLookup(t *testing.T) {
	store, path := openStore(t, "tokens.json")
	const (
		writers      = 8
		perWriter    = 12
		readers      = 8
		readerPasses = 40
	)

	type minted struct {
		secret string
		tenant string
		id     string
	}
	var mu sync.Mutex
	mints := make([]minted, 0, writers*perWriter)
	var wg sync.WaitGroup

	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			tenant := fmt.Sprintf("tenant-%d", w)
			for i := 0; i < perWriter; i++ {
				secret, token, err := store.Create(tenant, "ci", "concurrent")
				if err != nil {
					t.Errorf("Create in writer %d: %v", w, err)
					return
				}
				mu.Lock()
				mints = append(mints, minted{secret: secret, tenant: tenant, id: token.ID})
				mu.Unlock()
			}
		}(w)
	}
	for r := 0; r < readers; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < readerPasses; i++ {
				// Read the snapshot, then resolve every secret seen so far. A
				// secret that was minted must already be visible: the write of
				// the file and the write of the index happen under one lock.
				mu.Lock()
				snapshot := append([]minted(nil), mints...)
				mu.Unlock()
				for _, m := range snapshot {
					found, ok := store.Lookup(m.secret)
					if !ok {
						t.Errorf("minted secret %s of %s did not resolve", m.id, m.tenant)
						return
					}
					if found.Tenant != m.tenant {
						t.Errorf("secret %s resolved to tenant %q, want %q",
							m.id, found.Tenant, m.tenant)
						return
					}
				}
				_ = store.Len()
				_ = store.List()
			}
		}()
	}
	// A reload racing the writers must never corrupt the in-memory set: every
	// secret already minted still has to resolve after it.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			_ = store.Reload()
		}
	}()
	wg.Wait()

	if store.Len() != writers*perWriter {
		t.Fatalf("the store holds %d tokens, want %d", store.Len(), writers*perWriter)
	}
	seen := make(map[string]string, len(mints))
	for _, m := range mints {
		if prior, dup := seen[m.secret]; dup {
			t.Fatalf("two mints returned the same secret (tenants %q and %q)", prior, m.tenant)
		}
		seen[m.secret] = m.tenant
		found, ok := store.Lookup(m.secret)
		if !ok {
			t.Fatalf("after the run, secret %s did not resolve", m.id)
		}
		if found.Tenant != m.tenant {
			t.Fatalf("after the run, secret %s resolved to tenant %q, want %q",
				m.id, found.Tenant, m.tenant)
		}
	}
	// Every mint has to be on disk, not only in memory.
	reopened, err := OpenTokenStore(path)
	if err != nil {
		t.Fatalf("reopen %q: %v", path, err)
	}
	if reopened.Len() != writers*perWriter {
		t.Fatalf("the reopened store holds %d tokens, want %d",
			reopened.Len(), writers*perWriter)
	}
	for secret, tenant := range seen {
		found, ok := reopened.Lookup(secret)
		if !ok || found.Tenant != tenant {
			t.Fatalf("after reopen, secret of %q resolved to (%+v, %v)", tenant, found, ok)
		}
	}
}

func TestTokenStoreListIsOldestFirstAndNeverASecret(t *testing.T) {
	store, _ := openStore(t, "tokens.json")
	secrets := make([]string, 0, 4)
	ids := make([]string, 0, 4)
	for _, tenant := range []string{"one", "two", "three", "four"} {
		secret, token := mint(t, store, tenant, "ci", "note for "+tenant)
		secrets = append(secrets, secret)
		ids = append(ids, token.ID)
	}

	listed := store.List()
	if len(listed) != len(ids) {
		t.Fatalf("List() returned %d tokens, want %d", len(listed), len(ids))
	}
	for i, id := range ids {
		if listed[i].ID != id {
			t.Fatalf("List()[%d].ID = %q, want %q (oldest minted first)", i, listed[i].ID, id)
		}
		if listed[i].Hash != HashToken(secrets[i]) {
			t.Fatalf("List()[%d].Hash does not hash the secret it was minted as", i)
		}
		if listed[i].Tenant != listed[i].Namespace().Tenant {
			t.Fatalf("List()[%d] lost its namespace: %+v", i, listed[i])
		}
		for _, secret := range secrets {
			if listed[i].Hash == secret {
				t.Fatalf("List()[%d] carries a plaintext secret", i)
			}
		}
	}

	// List hands back a copy: a caller that sorts or truncates what it got must
	// not change what Lookup reads.
	listed[0] = Token{ID: "tok_tampered"}
	if store.List()[0].ID != ids[0] {
		t.Fatal("List() returned a slice the store still shares")
	}
	if _, ok := store.Lookup(secrets[0]); !ok {
		t.Fatal("the first secret stopped resolving")
	}
}

// writeTokenFile writes a token document the way another process might: JSON on
// disk, no store involved. createdAt is spelled the way JSON does.
func writeTokenFile(t *testing.T, path string, tokens []map[string]any) {
	t.Helper()
	document := map[string]any{"version": tokenFileVersion, "tokens": tokens}
	data, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		t.Fatalf("encode the token document: %v", err)
	}
	if err := os.WriteFile(path, append(data, '\n'), tokenFileMode); err != nil {
		t.Fatalf("write %q: %v", path, err)
	}
}
