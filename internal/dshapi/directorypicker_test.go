package dshapi

import (
	"os"
	"path/filepath"
	"testing"
)

// The directory picker reads a real filesystem, so these tests build one: what
// is being asserted is the handler's own rules -- absolute-path discipline, what
// counts as a row, the crumb chain, the cap, and the named refusals.

func directoryPickerFixture(t *testing.T) (*fixture, string) {
	t.Helper()
	f := newFixture(t, Config{})
	root := t.TempDir()
	for _, name := range []string{"alpha", "beta", ".config"} {
		if err := os.Mkdir(filepath.Join(root, name), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", name, err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("write notes.txt: %v", err)
	}
	if err := os.Symlink(filepath.Join(root, "alpha"), filepath.Join(root, "linked")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	return f, root
}

func TestDirectoryPickerListAnswersOneLevel(t *testing.T) {
	f, root := directoryPickerFixture(t)
	var listing DirectoryPickerListing
	decodeValue(t, f.post(t, "/api/directoryPicker/list",
		rpcBody(t, "s1", "directoryPicker/list", `{"path":`+jsonString(root)+`}`)), &listing)

	if listing.Path != root {
		t.Fatalf("path = %q, want the listed directory %q", listing.Path, root)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}
	if listing.Home != home {
		t.Fatalf("home = %q, want %q", listing.Home, home)
	}
	if listing.Truncated {
		t.Fatal("truncated = true, want false for a level under the cap")
	}

	// Directories only, name-sorted, hidden flagged, and a symlinked directory
	// is a row: the file beside them is not.
	names := make([]string, 0, len(listing.Entries))
	hidden := map[string]bool{}
	for _, entry := range listing.Entries {
		names = append(names, entry.Name)
		hidden[entry.Name] = entry.Hidden
		if entry.Path != filepath.Join(root, entry.Name) {
			t.Fatalf("entry %q path = %q, want an absolute path under the level", entry.Name, entry.Path)
		}
	}
	want := []string{".config", "alpha", "beta", "linked"}
	if len(names) != len(want) {
		t.Fatalf("entries = %v, want %v", names, want)
	}
	for i, name := range want {
		if names[i] != name {
			t.Fatalf("entries = %v, want %v", names, want)
		}
	}
	if !hidden[".config"] || hidden["alpha"] {
		t.Fatalf("hidden = %v, want only the dot-prefixed row hidden", hidden)
	}

	// Crumbs run from the filesystem root to the listed directory inclusive,
	// and the root crumb carries its full path as its name.
	if len(listing.Crumbs) < 2 {
		t.Fatalf("crumbs = %+v, want the root and the listed directory at least", listing.Crumbs)
	}
	if first, last := listing.Crumbs[0], listing.Crumbs[len(listing.Crumbs)-1]; first.Path != "/" || first.Name != "/" {
		t.Fatalf("first crumb = %+v, want the filesystem root", first)
	} else if last.Path != root || last.Name != filepath.Base(root) {
		t.Fatalf("last crumb = %+v, want the listed directory", last)
	}
	for _, crumb := range listing.Crumbs {
		if crumb.Hidden {
			t.Fatalf("crumb %+v is hidden, want every crumb visible", crumb)
		}
	}
}

func TestDirectoryPickerListDefaultsToTheHomeDirectory(t *testing.T) {
	f, _ := directoryPickerFixture(t)
	var listing DirectoryPickerListing
	decodeValue(t, f.post(t, "/api/directoryPicker/list",
		rpcBody(t, "s1", "directoryPicker/list", `{}`)), &listing)
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}
	if listing.Path != home || listing.Home != home {
		t.Fatalf("path = %q home = %q, want the host home %q with no path argument", listing.Path, listing.Home, home)
	}
}

func TestDirectoryPickerListReportsATruncatedLevel(t *testing.T) {
	f, root := directoryPickerFixture(t)
	previous := directoryPickerMaxEntries
	directoryPickerMaxEntries = 2
	t.Cleanup(func() { directoryPickerMaxEntries = previous })

	var listing DirectoryPickerListing
	decodeValue(t, f.post(t, "/api/directoryPicker/list",
		rpcBody(t, "s1", "directoryPicker/list", `{"path":`+jsonString(root)+`}`)), &listing)
	if !listing.Truncated || len(listing.Entries) != 2 {
		t.Fatalf("entries = %d truncated = %v, want the cap reported rather than a silent cut", len(listing.Entries), listing.Truncated)
	}
	if listing.Entries[0].Name != ".config" || listing.Entries[1].Name != "alpha" {
		t.Fatalf("entries = %+v, want the name-sorted head", listing.Entries)
	}
}

func TestDirectoryPickerListRefusesWhatItCannotList(t *testing.T) {
	f, root := directoryPickerFixture(t)
	file := filepath.Join(root, "notes.txt")
	tests := []struct {
		name string
		args string
	}{
		{name: "a relative path", args: `{"path":"relative/dir"}`},
		{name: "a path that does not exist", args: `{"path":` + jsonString(filepath.Join(root, "missing")) + `}`},
		{name: "a file rather than a directory", args: `{"path":` + jsonString(file) + `}`},
		{name: "an unknown argument", args: `{"path":` + jsonString(root) + `,"depth":2}`},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			envelope := decodeResponse(t, f.post(t, "/api/directoryPicker/list",
				rpcBody(t, "s1", "directoryPicker/list", testCase.args)))
			if envelope.Result.OK {
				t.Fatalf("result = %+v, want a refusal", envelope.Result.Value)
			}
			want := codeDirectoryUnreadable
			if testCase.name == "an unknown argument" {
				want = codeArgumentsInvalid
			}
			if envelope.Result.Error.Code != want {
				t.Fatalf("code = %q, want %q", envelope.Result.Error.Code, want)
			}
		})
	}
}

func TestDirectoryPickerCreateDirectory(t *testing.T) {
	f, root := directoryPickerFixture(t)
	parent := filepath.Join(root, "alpha")
	var created string
	decodeValue(t, f.post(t, "/api/directoryPicker/createDirectory",
		rpcBody(t, "s1", "directoryPicker/createDirectory",
			`{"path":`+jsonString(parent)+`,"name":"child"}`)), &created)
	if created != filepath.Join(parent, "child") {
		t.Fatalf("created = %q, want the child under the parent", created)
	}
	if info, err := os.Stat(created); err != nil || !info.IsDir() {
		t.Fatalf("stat %q: %v, want a created directory", created, err)
	}

	// The same child again is its own code: the browser shows "already exists"
	// rather than a generic failure.
	envelope := decodeResponse(t, f.post(t, "/api/directoryPicker/createDirectory",
		rpcBody(t, "s1", "directoryPicker/createDirectory",
			`{"path":`+jsonString(parent)+`,"name":"child"}`)))
	if envelope.Result.OK || envelope.Result.Error.Code != codeDirectoryExists {
		t.Fatalf("second create = %+v, want %s", envelope.Result.Error, codeDirectoryExists)
	}
}

func TestDirectoryPickerCreateDirectoryRefusals(t *testing.T) {
	f, root := directoryPickerFixture(t)
	file := filepath.Join(root, "notes.txt")
	tests := []struct {
		name string
		args string
		code string
	}{
		{name: "a separator in the name", args: `{"path":` + jsonString(root) + `,"name":"a/b"}`, code: codeDirectoryCreateFailed},
		{name: "a parent segment", args: `{"path":` + jsonString(root) + `,"name":".."}`, code: codeDirectoryCreateFailed},
		{name: "a blank name", args: `{"path":` + jsonString(root) + `,"name":" "}`, code: codeDirectoryCreateFailed},
		{name: "a relative parent", args: `{"path":"relative","name":"child"}`, code: codeDirectoryCreateFailed},
		{name: "a file as parent", args: `{"path":` + jsonString(file) + `,"name":"child"}`, code: codeDirectoryCreateFailed},
		{name: "an unknown argument", args: `{"path":` + jsonString(root) + `,"name":"child","mode":"0755"}`, code: codeArgumentsInvalid},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			envelope := decodeResponse(t, f.post(t, "/api/directoryPicker/createDirectory",
				rpcBody(t, "s1", "directoryPicker/createDirectory", testCase.args)))
			if envelope.Result.OK || envelope.Result.Error.Code != testCase.code {
				t.Fatalf("result = %+v, want code %q", envelope.Result.Error, testCase.code)
			}
		})
	}
}

// The native chooser opens on the *host's* display. This host has none, so the
// honest answer names the capability instead of leaving the console waiting.
func TestDirectoryPickerPickNamesTheMissingCapability(t *testing.T) {
	f, _ := directoryPickerFixture(t)
	envelope := decodeResponse(t, f.post(t, "/api/directoryPicker/pick",
		rpcBody(t, "s1", "directoryPicker/pick", `{}`)))
	if envelope.Result.OK || envelope.Result.Error.Code != codeUnimplemented {
		t.Fatalf("result = %+v, want an unimplemented refusal", envelope.Result.Error)
	}
	if capability, _ := envelope.Result.Error.Details["capability"].(string); capability == "" {
		t.Fatalf("details = %+v, want the missing capability named", envelope.Result.Error.Details)
	}
}
