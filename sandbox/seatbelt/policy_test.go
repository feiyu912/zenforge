package seatbelt

import (
	"strings"
	"testing"
)

func TestBuildPolicyIsClosedByDefault(t *testing.T) {
	built, err := BuildPolicy(Policy{
		WritableRoots: []string{"/Users/me/project", "/tmp/scratch"},
		ReadableRoots: []string{"/Users/me/data"},
		ReadOnlyPaths: []string{"/Users/me/secrets"},
	})
	if err != nil {
		t.Fatalf("BuildPolicy returned error: %v", err)
	}
	profile := built.Profile
	for _, want := range []string{
		"(version 1)",
		"(deny default)",
		`(allow file-write*`,
		`(path-ancestors (param "WRITABLE_ROOT_0"))`,
		`(subpath (param "WRITABLE_ROOT_0"))`,
		`(subpath (param "WRITABLE_ROOT_1"))`,
		`(subpath (param "READABLE_ROOT_0"))`,
		`(deny file-write* (subpath (param "READONLY_PATH_0")))`,
		"(allow process-exec)",
		`(sysctl-name "hw.ncpu")`,
		`(subpath "/System/Library/Frameworks")`,
		`(subpath "/usr/lib")`,
		`(subpath "/private/var/tmp")`,
	} {
		if !strings.Contains(profile, want) {
			t.Fatalf("profile is missing %q:\n%s", want, profile)
		}
	}
	// The profile text must not embed host paths: every path travels as a
	// -D parameter so a hostile path cannot rewrite the policy.
	if strings.Contains(profile, "/Users/me") || strings.Contains(profile, "/tmp/scratch") {
		t.Fatalf("profile leaked a host path:\n%s", profile)
	}
	if built.Params["WRITABLE_ROOT_0"] != "/Users/me/project" || built.Params["READONLY_PATH_0"] != "/Users/me/secrets" {
		t.Fatalf("params = %#v", built.Params)
	}
	// Network is denied unless asked for: no outbound allowance, and no
	// network capability rule at all.
	if strings.Contains(profile, "(allow network-outbound)") || strings.Contains(profile, "(allow network-inbound)") {
		t.Fatalf("profile granted network by default:\n%s", profile)
	}
}

func TestBuildPolicyProtectsGitAndConfigInsideWritableRoots(t *testing.T) {
	built, err := BuildPolicy(Policy{WritableRoots: []string{"/work/repo"}})
	if err != nil {
		t.Fatalf("BuildPolicy returned error: %v", err)
	}
	for index, name := range defaultProtectedNames {
		key := "PROTECTED_0_" + string(rune('0'+index))
		want := "/work/repo/" + name
		if built.Params[key] != want {
			t.Fatalf("params[%s] = %q, want %q", key, built.Params[key], want)
		}
		// The protected path is excluded from the allow rule, so a write to
		// it is denied by the closed-by-default policy.
		if !strings.Contains(built.Profile, `(require-not (literal (param "`+key+`")))`) ||
			!strings.Contains(built.Profile, `(require-not (subpath (param "`+key+`")))`) {
			t.Fatalf("profile does not protect %s:\n%s", name, built.Profile)
		}
	}
	// The root itself cannot be unlinked or replaced.
	if !strings.Contains(built.Profile, `(deny file-write-unlink (require-all (literal (param "WRITABLE_ROOT_0")) (vnode-type DIRECTORY)))`) {
		t.Fatalf("profile does not anchor the writable root:\n%s", built.Profile)
	}

	custom, err := BuildPolicy(Policy{WritableRoots: []string{"/work/repo"}, ProtectedNames: []string{".git"}})
	if err != nil {
		t.Fatalf("BuildPolicy returned error: %v", err)
	}
	if strings.Contains(custom.Profile, "PROTECTED_0_1") {
		t.Fatalf("custom protected names were ignored: %#v", custom.Params)
	}
}

func TestBuildPolicyGrantsNetworkOnlyOnRequest(t *testing.T) {
	built, err := BuildPolicy(Policy{WritableRoots: []string{"/work"}, AllowNetwork: true, AllowLocalBinding: true})
	if err != nil {
		t.Fatalf("BuildPolicy returned error: %v", err)
	}
	if !strings.Contains(built.Profile, "(allow network-outbound)") || !strings.Contains(built.Profile, "(allow network-bind (local ip") {
		t.Fatalf("network was not granted:\n%s", built.Profile)
	}
	bindingOnly, err := BuildPolicy(Policy{WritableRoots: []string{"/work"}, AllowNetwork: true})
	if err != nil {
		t.Fatalf("BuildPolicy returned error: %v", err)
	}
	if strings.Contains(bindingOnly.Profile, "(allow network-bind") {
		t.Fatal("network-bind was granted without being requested")
	}
}

func TestBuildPolicyAppendsCallerRules(t *testing.T) {
	built, err := BuildPolicy(Policy{WritableRoots: []string{"/work"}, ExtraRules: "(allow file-read* (literal \"/etc/hosts\"))"})
	if err != nil {
		t.Fatalf("BuildPolicy returned error: %v", err)
	}
	if !strings.HasSuffix(strings.TrimSpace(built.Profile), `(allow file-read* (literal "/etc/hosts"))`) {
		t.Fatalf("caller rules were not appended:\n%s", built.Profile)
	}
}

func TestBuildPolicyRejectsUnusableInput(t *testing.T) {
	cases := map[string]Policy{
		"relative root":      {WritableRoots: []string{"work"}},
		"filesystem root":    {WritableRoots: []string{"/"}},
		"relative read path": {ReadableRoots: []string{"data"}},
		"relative read-only": {ReadOnlyPaths: []string{"secret"}},
		"path in a name":     {WritableRoots: []string{"/work"}, ProtectedNames: []string{".git/HEAD"}},
		"empty name":         {WritableRoots: []string{"/work"}, ProtectedNames: []string{"  "}},
	}
	for name, policy := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := BuildPolicy(policy); err == nil {
				t.Fatalf("policy %#v was accepted", policy)
			}
		})
	}
}

func TestBuildPolicyDeduplicatesRoots(t *testing.T) {
	built, err := BuildPolicy(Policy{WritableRoots: []string{"/work", "/work/", "/work/../work"}})
	if err != nil {
		t.Fatalf("BuildPolicy returned error: %v", err)
	}
	if _, ok := built.Params["WRITABLE_ROOT_1"]; ok {
		t.Fatalf("duplicate roots were not collapsed: %#v", built.Params)
	}
}

func TestBuiltPolicyArgumentsBindParameters(t *testing.T) {
	built, err := BuildPolicy(Policy{WritableRoots: []string{"/work"}, ReadOnlyPaths: []string{"/secret"}})
	if err != nil {
		t.Fatalf("BuildPolicy returned error: %v", err)
	}
	args := built.Argument()
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "-D READONLY_PATH_0=/secret") || !strings.Contains(joined, "-D WRITABLE_ROOT_0=/work") {
		t.Fatalf("arguments = %v", args)
	}
	if args[len(args)-2] != "-p" || args[len(args)-1] != built.Profile {
		t.Fatalf("profile must be last: %v", args)
	}
	// Parameters are sorted, so the argument list is deterministic.
	first := strings.Join(built.Argument(), " ")
	second := strings.Join(built.Argument(), " ")
	if first != second {
		t.Fatalf("arguments are not deterministic: %q vs %q", first, second)
	}
	empty, err := BuildPolicy(Policy{})
	if err != nil {
		t.Fatalf("BuildPolicy returned error: %v", err)
	}
	if got := empty.Argument(); len(got) != 2 || got[0] != "-p" {
		t.Fatalf("empty policy arguments = %v", got)
	}
}
