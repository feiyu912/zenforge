package dshapi

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Wire shapes for POST /api/credentials/{describe,set,unset}. The console's
// Models page reads credential state for the references its rows name and
// stores the value an operator types (packages/client/ui-settings-models/src/
// client/store.ts:215-222 and operations.ts:83-95). The host owner upstream is
// packages/api/settings-controller/src/credentials.ts, whose contract has two
// properties this file must preserve: describe redacts at the source, and a
// secret crosses in one direction only -- no method here returns a value.
//
// The host has exactly one model credential (ADR 0084), so every reference the
// panel asks about is answered from that one credential, and set stores under
// it whichever reference was named. That simplification is deliberate and
// documented rather than hidden: this harness models no provider-scoped
// credential store, and inventing one that holds nothing would be worse.

// CredentialInfo is one reference's state as a configuration surface renders
// it. Upstream credentials.ts:44-52 (projectCredentialInfo) copies exactly
// these fields: a provider whose describe carried extra enumerable properties
// must not leak them to the caller.
type CredentialInfo struct {
	// Configured reports whether a value would be found. It is the only thing
	// the console learns about the credential.
	Configured bool `json:"configured"`
	// Source names where the value comes from, when the host can say. This host
	// answers "settings": the credential lives in the process's settings store.
	Source string `json:"source,omitempty"`
	// Writable reports whether this surface may change the credential.
	Writable bool `json:"writable"`
}

// maxDescribeCredentialRefs mirrors upstream's MAX_DESCRIBE_REFS
// (credentials.ts:21-24): a settings page asks about the references its own
// rows name, so this bound is far above any real page and still keeps one
// request from starting unbounded provider work.
const maxDescribeCredentialRefs = 64

// credentialRefSource is what this host reports as the origin of the single
// credential it holds.
const credentialRefSource = "settings"

// credentialRefPattern is the wire's reference grammar (credentials.ts:26),
// which is what keeps a reference usable as an environment-variable name.
var credentialRefPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// CredentialStore is the injected credential face. It is an interface rather
// than a function type because three operations share one dependency, and it is
// injected for the same reason the model catalog is: this package cannot import
// the serve command, which would cycle. A nil store means every credential
// method answers unimplemented rather than pretending a credential exists.
type CredentialStore interface {
	// CredentialConfigured reports whether a value would be found, whether it
	// came from a stored value or the environment the process was started with.
	// It must never reveal which, and must never return the value.
	CredentialConfigured() bool
	// StoreCredential records a non-empty value as this host's model credential.
	StoreCredential(value string) error
	// RemoveCredential forgets a stored value. It may leave an
	// environment-supplied credential in place, which the next
	// CredentialConfigured call reports honestly.
	RemoveCredential() error
}

// SetCredentials installs the store the credentials methods answer from. It is
// separate from New so the serve command can supply its settings store without
// changing New's signature, and passing nil restores the unimplemented answer.
// Safe to call while requests are in flight.
func (h *Handler) SetCredentials(store CredentialStore) {
	h.credentialsMu.Lock()
	h.credentials = store
	h.credentialsMu.Unlock()
}

// credentialStore reads the installed store under the lock, so a settings
// change during a request is not a data race.
func (h *Handler) credentialStore() CredentialStore {
	h.credentialsMu.RLock()
	defer h.credentialsMu.RUnlock()
	return h.credentials
}

// credentialsDescribe answers POST /api/credentials/describe with one view per
// requested reference, keyed by that reference (credentials.ts:84-91 returns
// Object.fromEntries over the requested names).
func (h *Handler) credentialsDescribe(_ context.Context, args map[string]json.RawMessage) (any, *methodError) {
	refs, failure := credentialRefsArgument(args)
	if failure != nil {
		return nil, failure
	}
	store := h.credentialStore()
	if store == nil {
		return nil, credentialDependencyMissing("credentials/describe")
	}
	configured := store.CredentialConfigured()
	views := make(map[string]CredentialInfo, len(refs))
	for _, ref := range refs {
		views[ref] = CredentialInfo{
			Configured: configured,
			Source:     credentialRefSource,
			Writable:   true,
		}
	}
	return views, nil
}

// credentialsSet answers POST /api/credentials/set. The value is never echoed,
// never logged and never returned: the reply is an ok envelope with no value, as
// upstream's `set(ref, value): Promise<void>` declares.
func (h *Handler) credentialsSet(_ context.Context, args map[string]json.RawMessage) (any, *methodError) {
	if failure := rejectUnknownArguments(args, "ref", "value"); failure != nil {
		return nil, failure
	}
	ref, present, failure := stringArg(args, "ref")
	if failure != nil {
		return nil, failure
	}
	if !present || !credentialRefPattern.MatchString(ref) {
		return nil, fail(codeBadRequest,
			`argument "ref" must match ^[A-Za-z_][A-Za-z0-9_]*$`,
			map[string]any{"argument": "ref"})
	}
	value, present, failure := stringArg(args, "value")
	if failure != nil {
		return nil, failure
	}
	if !present || value == "" {
		return nil, fail(codeBadRequest,
			`argument "value" must be a non-empty string`,
			map[string]any{"argument": "value"})
	}
	store := h.credentialStore()
	if store == nil {
		return nil, credentialDependencyMissing("credentials/set")
	}
	if err := store.StoreCredential(value); err != nil {
		// The refusal is reported without the secret: a provider error string is
		// not trusted to be free of the value it was just handed.
		return nil, fail(codeInternal,
			"the host refused to store the credential: "+redactSecret(err.Error(), value),
			map[string]any{"reference": ref})
	}
	return nil, nil
}

// credentialsUnset answers POST /api/credentials/unset, whose upstream shape is
// also Promise<void>.
func (h *Handler) credentialsUnset(_ context.Context, args map[string]json.RawMessage) (any, *methodError) {
	if failure := rejectUnknownArguments(args, "ref"); failure != nil {
		return nil, failure
	}
	ref, present, failure := stringArg(args, "ref")
	if failure != nil {
		return nil, failure
	}
	if !present || !credentialRefPattern.MatchString(ref) {
		return nil, fail(codeBadRequest,
			`argument "ref" must match ^[A-Za-z_][A-Za-z0-9_]*$`,
			map[string]any{"argument": "ref"})
	}
	store := h.credentialStore()
	if store == nil {
		return nil, credentialDependencyMissing("credentials/unset")
	}
	if err := store.RemoveCredential(); err != nil {
		return nil, fail(codeInternal,
			"the host refused to remove the credential: "+err.Error(),
			map[string]any{"reference": ref})
	}
	return nil, nil
}

// credentialRefsArgument reads the required `refs` array and enforces the
// grammar and the batch bound. Upstream answers gateway/bad-request with the
// parse issues attached (credentials.ts:27-33), so the details carry them.
func credentialRefsArgument(args map[string]json.RawMessage) ([]string, *methodError) {
	if failure := rejectUnknownArguments(args, "refs"); failure != nil {
		return nil, failure
	}
	raw, ok := args["refs"]
	if !ok {
		return nil, argumentRequired("refs")
	}
	var refs []string
	if err := json.Unmarshal(raw, &refs); err != nil {
		return nil, fail(codeBadRequest, `argument "refs" must be an array of strings`,
			map[string]any{"argument": "refs"})
	}
	if len(refs) > maxDescribeCredentialRefs {
		return nil, fail(codeBadRequest,
			fmt.Sprintf("argument \"refs\" must name at most %d references", maxDescribeCredentialRefs),
			map[string]any{"argument": "refs", "limit": maxDescribeCredentialRefs})
	}
	for index, ref := range refs {
		if !credentialRefPattern.MatchString(ref) {
			return nil, fail(codeBadRequest,
				fmt.Sprintf("argument \"refs\" entry %d is not a valid reference name", index),
				map[string]any{"argument": "refs", "index": index})
		}
	}
	return refs, nil
}

// rejectUnknownArguments refuses any argument the method does not declare,
// naming one field and never echoing a value. The upstream gateway rejects
// unexpected fields rather than ignoring them (packages/api/gateway/src/
// index.ts:1107-1132), and a typo silently ignored is a settings write that
// appears to work. Names are sorted so a request with several extras reports
// deterministically.
func rejectUnknownArguments(args map[string]json.RawMessage, allowed ...string) *methodError {
	permitted := make(map[string]struct{}, len(allowed))
	for _, name := range allowed {
		permitted[name] = struct{}{}
	}
	unexpected := make([]string, 0, len(args))
	for name := range args {
		if _, ok := permitted[name]; !ok {
			unexpected = append(unexpected, name)
		}
	}
	if len(unexpected) == 0 {
		return nil
	}
	sort.Strings(unexpected)
	return fail(codeArgumentsInvalid,
		fmt.Sprintf("unexpected argument %q", unexpected[0]),
		map[string]any{"argument": unexpected[0]})
}

// redactSecret removes a secret from a diagnostic string. It is defensive: the
// message it guards is built by code outside this package, and a credential that
// reaches a log line is the one failure this file exists to prevent.
func redactSecret(message, secret string) string {
	if secret == "" {
		return message
	}
	return strings.ReplaceAll(message, secret, "[redacted]")
}

// credentialDependencyMissing is the honest answer when no store is installed:
// the console's credential badge shows a failure instead of a state this host
// cannot verify.
func credentialDependencyMissing(method string) *methodError {
	return fail(codeUnimplemented,
		method+" is not configured: the host has no credential store; the serve command must install one with Handler.SetCredentials",
		map[string]any{"dependency": "CredentialStore"})
}
