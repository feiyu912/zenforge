package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/feiyu912/zenforge/configlayer"
	"github.com/feiyu912/zenforge/internal/dshapi"
)

// The console's settings document (ADR 0102).
//
// Everything the console and the operator configure through it -- the endpoint,
// the model, the credential, the declared provider profiles, the namespaces the
// console owns, and the revision each namespace is fenced with -- has lived in the
// running process, so a restart costs a re-typed API key. This file is the durable
// half: one host-owned document, written `0600` and renamed into place, read back
// at startup.
//
// Four rules hold it together, and each is asserted in a test:
//
//   - It is never the repository or the served workspace. The console reads
//     workspace files back to a browser, so a document that sat inside one would
//     hand its credential to any page that asked.
//   - It is the only place the credential goes. Not a log line, not an error
//     string, not an event, not `settings/describe` (ADR 0084).
//   - A failure to read it names the file and the kind of damage, never its
//     contents: the standard JSON error quotes the value it could not decode, and
//     the value it is failing on may be exactly that credential.
//   - A document that cannot be read stops the host. Starting with an empty Models
//     page and no key is the silent degradation this tier refuses everywhere else.

const (
	// consoleSettingsFileName is the document's name inside the host's own
	// configuration directory.
	consoleSettingsFileName = "console-settings.json"
	// consoleSettingsFileVersion is the document version this host reads and
	// writes. A future shape bumps it; a file written by a newer host is refused
	// rather than half-read, and an unversioned file is refused as not a document.
	consoleSettingsFileVersion = 1
	// consoleSettingsFileMode is the document's permission: owner read and write
	// only, because it holds the credential.
	consoleSettingsFileMode os.FileMode = 0o600
	// consoleSettingsDirMode is the mode the directory is created with when the
	// host has to make it.
	consoleSettingsDirMode os.FileMode = 0o700
	// consoleSettingsTempPattern names the staging file. The dot prefix and the
	// .tmp suffix keep it out of any glob a reader runs over the directory: an
	// interrupted write must not read as a second document.
	consoleSettingsTempPattern = ".console-settings-*.json.tmp"
)

// consoleSettingsFile is the document as it exists on disk. The four settings
// fields are pointers, not plain strings, because the document has to distinguish
// "this host was never told" from "the console cleared this field": an operator who
// removes the endpoint override must not find `--base-url` applied again on the
// next start, and a field the document leaves out must not wipe the flag the host
// was started with either. The maps and slices are the rest of the console-written
// state, and the revisions belong here for the reason ADR 0087 gives: a fence
// number that resets on restart is not a fence number.
type consoleSettingsFile struct {
	Version int `json:"version"`
	// Provider, Model, BaseURL and APIKey are the console-written model
	// configuration. A field is present only when the console wrote it -- a value
	// the host was merely started with is the seed and stays out, or the file
	// would claim the operator saved a flag (ADR 0103). APIKey is the only secret
	// this document holds and the only place this host writes it.
	Provider *string `json:"provider,omitempty"`
	Model    *string `json:"model,omitempty"`
	BaseURL  *string `json:"baseUrl,omitempty"`
	APIKey   *string `json:"apiKey,omitempty"`
	// Revisions is each namespace's current revision, keyed by namespace.
	Revisions map[string]int64 `json:"revisions,omitempty"`
	// ConsoleSections holds the namespaces the console owns (ADR 0094).
	ConsoleSections map[string]map[string]any `json:"consoleSections,omitempty"`
	// ProviderProfiles holds the hand-declared routes in declaration order
	// (ADR 0095).
	ProviderProfiles []consoleProfileRecord `json:"providerProfiles,omitempty"`
	// ConsoleRoutes records the provider namespace each console-written settings
	// field above was written through, keyed by field name. A value the console
	// saved belongs to the card it was saved on, so the page shows it back on that
	// card and not on another; a document written before this existed carries no
	// routes, and its fields are attributed to the provider the host is configured
	// with (ADR 0103).
	ConsoleRoutes map[string]string `json:"consoleRoutes,omitempty"`
	// ModelSelections holds the model each session's operator chose, keyed by
	// session id. A session that never chose one is absent rather than recorded
	// as an empty choice, so a restart restores exactly the selections that were
	// made and nothing else (ADR 0103).
	ModelSelections map[string]consoleSelectionRecordFile `json:"modelSelections,omitempty"`
}

// consoleSelectionRecordFile is one session's chosen model as the document stores
// it. The provider and model are what the composer shows and what the next run
// installs; the runtime last-used hint is not here, because it changes on every
// run and would rewrite the document for a label.
type consoleSelectionRecordFile struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

// consoleProfileRecord is one declared provider profile as the document stores it.
// dshapi.ProviderProfile carries its route id outside the JSON object -- the
// console addresses a profile by dict key -- so the document names the key
// explicitly instead of re-deriving an order from a map.
type consoleProfileRecord struct {
	Provider    string                 `json:"provider"`
	DisplayName string                 `json:"displayName,omitempty"`
	APIKeyEnv   string                 `json:"apiKeyEnv,omitempty"`
	API         string                 `json:"api,omitempty"`
	BaseURL     string                 `json:"baseURL,omitempty"`
	Models      []dshapi.ProviderModel `json:"models,omitempty"`
}

// consoleSettingsWriter owns one document: the path it lives at, how its bytes are
// produced, and whether the host has ever successfully read or written it. The
// stores built in serve.go hand it a snapshot closure rather than a copy of their
// state, so exactly one object decides what "durable" means while each store stays
// the owner of what is live.
type consoleSettingsWriter struct {
	path     string
	snapshot func() consoleSettingsFile

	mu      sync.Mutex
	durable bool
}

func newConsoleSettingsWriter(path string, snapshot func() consoleSettingsFile) *consoleSettingsWriter {
	return &consoleSettingsWriter{path: path, snapshot: snapshot}
}

// markLoaded records that a document was read at startup: it exists, so
// `settings/describe` says so, whether or not this process has ever written it.
func (w *consoleSettingsWriter) markLoaded() {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.durable = true
}

// save writes the current snapshot. A nil writer, or a host with no configured
// path, has nothing to write and says so by returning nil: that is the
// process-local host, and its startup line already named the missing directory.
//
// A failed write is both logged and returned. The change is already live in this
// process by the time the store calls this, so the store reports the refusal on
// the wire too -- an operator must not be told a setting is saved when the file it
// would be restored from does not have it.
func (w *consoleSettingsWriter) save() error {
	if w == nil || strings.TrimSpace(w.path) == "" {
		return nil
	}
	file := w.snapshot()
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := writeConsoleSettingsFile(w.path, file); err != nil {
		slog.Warn("the console settings document could not be written: the change is live in this process but will not survive a restart",
			"path", w.path, "error", err)
		return err
	}
	w.durable = true
	return nil
}

// hasDocument is the `hasDocument` flag of settings/describe: a document this host
// has read or written exists, and the console may treat this host's configuration
// as durable.
func (w *consoleSettingsWriter) hasDocument() bool {
	if w == nil {
		return false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.durable
}

// name names the file, for the startup log line and the error text.
func (w *consoleSettingsWriter) name() string {
	if w == nil {
		return ""
	}
	return w.path
}

// consoleSettingsPath decides where the document lives. An explicit
// --settings-file is the operator's decision and is taken verbatim; otherwise the
// file goes into the host's configuration directory -- the same one the CLI's user
// config layer already reads, so a host does not grow a second place its operator
// has to know about, and the same one `ZENFORGE_CONFIG_DIR` moves. An empty path
// with a nil error means this host has no durable home for settings: no
// configuration directory could be found, and it says so at startup rather than
// writing somewhere surprising.
func consoleSettingsPath(explicit string) (string, error) {
	if path := strings.TrimSpace(explicit); path != "" {
		absolute, err := filepath.Abs(path)
		if err != nil {
			return "", fmt.Errorf("resolve --settings-file %q: %w", path, err)
		}
		return absolute, nil
	}
	configDir, err := configlayer.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("find the host configuration directory: %w", err)
	}
	if strings.TrimSpace(configDir) == "" {
		return "", nil
	}
	return filepath.Join(configDir, consoleSettingsFileName), nil
}

// refuseSettingsFileInWorkspace stops the document from being placed where the
// console can already read it back. The file sidebar serves paths under the
// workspace root (ADR 0089), so a document inside one -- which is exactly what a
// relative `--settings-file console-settings.json` produces, since the workspace
// defaults to the working directory -- would expose the credential as a browsable
// file. The repository checkout is the common case of that.
func refuseSettingsFileInWorkspace(path, workspace string) error {
	if strings.TrimSpace(path) == "" || strings.TrimSpace(workspace) == "" {
		return nil
	}
	// Cleaned absolute paths are compared without resolving links: a document
	// that is not on disk yet cannot be evaluated, and a path that textually
	// lands inside the workspace is the case being refused.
	document := filepath.Clean(path)
	root := filepath.Clean(workspace)
	relative, err := filepath.Rel(root, document)
	if err != nil {
		return nil
	}
	if filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return nil
	}
	return fmt.Errorf("--settings-file %s is inside the workspace this host serves (%s), and the console reads workspace files back to the browser: keep the settings document outside the workspace, or point the host at another directory with --workspace", document, root)
}

// loadConsoleSettingsFile reads the document. The second result says whether a
// document was there at all: "no file" is a normal start -- the host was never
// configured through the console -- while "a file this host cannot read" is not,
// and the two must not collapse into the same answer.
func loadConsoleSettingsFile(path string) (consoleSettingsFile, bool, error) {
	if strings.TrimSpace(path) == "" {
		return consoleSettingsFile{}, false, nil
	}
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return consoleSettingsFile{}, false, nil
	}
	if err != nil {
		return consoleSettingsFile{}, false, fmt.Errorf("settings document %s could not be read: %w", path, describeSettingsReadError(err))
	}
	if info.IsDir() {
		return consoleSettingsFile{}, false, fmt.Errorf("settings document %s is a directory; point --settings-file at a file, or move the directory aside", path)
	}
	if mode := info.Mode().Perm(); mode&0o077 != 0 {
		return consoleSettingsFile{}, false, fmt.Errorf("settings document %s can be read by %s (mode %04o), and it holds this host's API key; run chmod 600 on it, or move it aside and restart", path, settingsFileReaders(mode), mode)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return consoleSettingsFile{}, false, fmt.Errorf("settings document %s could not be read: %w", path, describeSettingsReadError(err))
	}
	var file consoleSettingsFile
	if err := decodeConsoleSettingsFile(data, &file); err != nil {
		return consoleSettingsFile{}, false, fmt.Errorf("settings document %s %s; fix it or move it aside and restart this host", path, err)
	}
	if file.Version > consoleSettingsFileVersion {
		return consoleSettingsFile{}, false, fmt.Errorf("settings document %s was written by a newer zenforge (document version %d; this host reads %d); start the host that wrote it, or move the file aside", path, file.Version, consoleSettingsFileVersion)
	}
	if file.Version < consoleSettingsFileVersion {
		return consoleSettingsFile{}, false, fmt.Errorf("settings document %s is not a zenforge settings document (document version %d; this host reads %d); fix it or move it aside and restart", path, file.Version, consoleSettingsFileVersion)
	}
	return file, true, nil
}

// decodeConsoleSettingsFile parses the document strictly: an unknown key is
// refused, because a field this host does not know is a field whose value would be
// silently dropped on the next write.
func decodeConsoleSettingsFile(data []byte, into *consoleSettingsFile) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(into); err != nil {
		return describeSettingsDecodeError(err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("holds more than one JSON object")
	}
	return nil
}

// describeSettingsDecodeError turns a decode failure into a sentence that is safe
// to print. The distinction matters: encoding/json quotes the value it could not
// decode, and the value it is failing on may be the credential. So a type error
// contributes the field it landed on and nothing else.
func describeSettingsDecodeError(err error) error {
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &typeErr) {
		field := typeErr.Field
		if field == "" {
			field = typeErr.Type.String()
		}
		return fmt.Errorf("has a value at %q of a type this host does not store", field)
	}
	var syntaxErr *json.SyntaxError
	if errors.As(err, &syntaxErr) {
		return fmt.Errorf("is not valid JSON (at byte %d)", syntaxErr.Offset)
	}
	if strings.Contains(err.Error(), "unknown field") {
		// The message names the key, never the value, which is what a strict
		// decoder reports.
		return fmt.Errorf("has a field this host does not store (%s)", err)
	}
	return fmt.Errorf("could not be parsed (%T)", err)
}

// describeSettingsReadError keeps a filesystem failure short: an os.PathError has
// already named the path once, and naming it twice in one line reads as two files.
func describeSettingsReadError(err error) error {
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		return fmt.Errorf("%s: %s", pathErr.Op, pathErr.Err)
	}
	return err
}

// settingsFileReaders names who a lax mode lets in, for the message that refuses
// the file.
func settingsFileReaders(mode os.FileMode) string {
	switch {
	case mode&0o004 != 0:
		return "everyone"
	case mode&0o070 != 0:
		return "its group"
	default:
		return "someone other than its owner"
	}
}

// writeConsoleSettingsFile writes the document atomically: the bytes go to a
// staging file in the same directory at `0600`, and only then is the document
// renamed onto its name. A reader therefore sees either the previous document or
// the new one, never a half-written file -- and the mode is set before the rename,
// so the credential is never briefly readable by others under a permissive umask.
func writeConsoleSettingsFile(path string, file consoleSettingsFile) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("write the settings document: no path was configured")
	}
	file.Version = consoleSettingsFileVersion
	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return fmt.Errorf("encode the settings document for %s: %w", path, err)
	}
	data = append(data, '\n')
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, consoleSettingsDirMode); err != nil {
		return fmt.Errorf("create the directory for %s: %w", directory, err)
	}
	staging, err := os.CreateTemp(directory, consoleSettingsTempPattern)
	if err != nil {
		return fmt.Errorf("stage the settings document beside %s: %w", path, err)
	}
	staged := staging.Name()
	// Once the rename succeeds the staged name is gone; removing it then is a
	// no-op, so this cleanup only ever erases a failed write's leftover.
	defer func() { _ = os.Remove(staged) }()
	if err := staging.Chmod(consoleSettingsFileMode); err != nil {
		_ = staging.Close()
		return fmt.Errorf("set the mode of the staged settings document: %w", err)
	}
	if _, err := staging.Write(data); err != nil {
		_ = staging.Close()
		return fmt.Errorf("write the settings document for %s: %w", path, err)
	}
	if err := staging.Sync(); err != nil {
		_ = staging.Close()
		return fmt.Errorf("flush the settings document for %s: %w", path, err)
	}
	if err := staging.Close(); err != nil {
		return fmt.Errorf("close the staged settings document: %w", err)
	}
	if err := os.Rename(staged, path); err != nil {
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
}

// newConsoleSettingsDocument wires one host's document: it decides where the file
// lives, refuses a place the console could already read back, opens the writer, and
// loads what is already on disk into the stores the writer snapshots.
//
// A load failure is returned for the caller to refuse startup with: a document that
// cannot be read is not the same as one that does not exist, and starting anyway
// would show an operator a Models page that has lost their endpoint, their profiles
// and their key.
func newConsoleSettingsDocument(explicit, workspace string, settings *settingsStore, profiles *consoleProviderProfiles) (*consoleSettingsWriter, error) {
	path, err := consoleSettingsPath(explicit)
	if err != nil {
		return nil, err
	}
	if err := refuseSettingsFileInWorkspace(path, workspace); err != nil {
		return nil, err
	}
	if strings.TrimSpace(path) == "" {
		slog.Warn("the console's settings will not survive a restart: this host has no configuration directory to keep them in; pass --settings-file to name one")
		return newConsoleSettingsWriter("", settings.documentFile), nil
	}
	writer := newConsoleSettingsWriter(path, settings.documentFile)
	settings.document = writer
	profiles.document = writer
	file, found, err := loadConsoleSettingsFile(path)
	if err != nil {
		return nil, err
	}
	if !found {
		slog.Info("the console settings document does not exist yet; the first settings write will create it", "path", path)
		return writer, nil
	}
	applyConsoleSettingsDocument(file, settings, profiles)
	writer.markLoaded()
	if shadowed := settingsShadowedByDocument(file, settings); len(shadowed) > 0 {
		// Only the field names: the value the flag carried may itself be the
		// credential this document has now replaced.
		slog.Info("the console settings document overrides this host's startup configuration",
			"path", path, "fields", strings.Join(shadowed, ","))
	}
	slog.Info("loaded the console settings document", "path", path)
	return writer, nil
}

// settingsShadowedByDocument names the startup fields a loaded document overwrote,
// so an operator who restarted with a flag that no longer applies learns which one
// and why the page still shows the other value. A field the seed left empty is not
// shadowed -- the document filled a hole rather than overriding a decision -- and
// the credential is named as a field and never as a value.
func settingsShadowedByDocument(file consoleSettingsFile, settings *settingsStore) []string {
	// applyDocument has already copied the document over the seed, so the
	// comparison is between the document and what the seed held before it.
	var shadowed []string
	if settings == nil {
		return shadowed
	}
	settings.mu.RLock()
	seed := settings.seed
	settings.mu.RUnlock()
	checks := []struct {
		field     string
		fromFlags string
		fromDoc   *string
	}{
		{"baseUrl", seed.baseURL, file.BaseURL},
		{"model", seed.model, file.Model},
		{"provider", seed.provider, file.Provider},
		{"api-key", seed.apiKey, file.APIKey},
	}
	for _, check := range checks {
		if check.fromFlags == "" || check.fromDoc == nil || *check.fromDoc == check.fromFlags {
			continue
		}
		shadowed = append(shadowed, check.field)
	}
	return shadowed
}

// applyConsoleSettingsDocument copies a loaded document into the live stores. The
// document is authoritative for what it names and silent about what it leaves out,
// which is why its settings fields are pointers.
func applyConsoleSettingsDocument(file consoleSettingsFile, settings *settingsStore, profiles *consoleProviderProfiles) {
	if settings == nil {
		return
	}
	settings.applyDocument(file)
	if profiles != nil {
		profiles.applyDocument(file.ProviderProfiles)
	}
}

// profileRecords renders the declared profiles as the document stores them.
func profileRecords(listed []dshapi.ProviderProfileStatus) []consoleProfileRecord {
	if len(listed) == 0 {
		return nil
	}
	records := make([]consoleProfileRecord, 0, len(listed))
	for _, status := range listed {
		profile := status.Profile
		records = append(records, consoleProfileRecord{
			Provider:    profile.Provider,
			DisplayName: profile.DisplayName,
			APIKeyEnv:   profile.APIKeyEnv,
			API:         profile.API,
			BaseURL:     profile.BaseURL,
			Models:      profile.Models,
		})
	}
	return records
}

// declaredProfiles renders stored records back into the profile shape, keeping
// declaration order and dropping a record with no route to declare it under. A
// profile this host cannot serve is kept: it is reported with the same
// serviceability reason as one written through the console (ADR 0095), and a
// hand-edited document is the operator's own.
func declaredProfiles(records []consoleProfileRecord) []dshapi.ProviderProfile {
	declared := make([]dshapi.ProviderProfile, 0, len(records))
	for _, record := range records {
		route := strings.TrimSpace(record.Provider)
		if route == "" {
			continue
		}
		declared = append(declared, dshapi.ProviderProfile{
			Provider:    route,
			DisplayName: record.DisplayName,
			APIKeyEnv:   record.APIKeyEnv,
			API:         record.API,
			BaseURL:     record.BaseURL,
			Models:      record.Models,
		})
	}
	if len(declared) == 0 {
		return nil
	}
	return declared
}
