package skill

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"

	"github.com/feiyu912/zenforge/tool"
)

// Bundle is an immutable, concurrency-safe snapshot of skill discovery.
// Construct it once during application startup and reuse it across model calls.
// Both descriptors and content are bound at construction. The body remains
// absent from the prompt and is disclosed from memory only when the tool runs.
type Bundle struct {
	prompt      string
	tool        tool.Tool
	fingerprint string
}

// Options controls bundle advertisement limits. Non-zero values may only make
// the defaults stricter.
type Options struct {
	MaxAdvertisedEntries       int
	MaxAdvertisedMetadataBytes int
}

// NewBundle discovers and validates descriptors, applies allowlist, and builds
// the prompt and load_skill tool. A nil allowlist permits every discovered
// skill; a non-nil empty allowlist permits none.
func NewBundle(ctx context.Context, catalog Catalog, allowlist []string, configured ...Options) (*Bundle, error) {
	if catalog == nil {
		return nil, fmt.Errorf("%w: nil catalog", ErrInvalid)
	}
	options, err := resolveOptions(configured)
	if err != nil {
		return nil, err
	}
	items, err := catalog.List(ctx)
	if err != nil {
		return nil, err
	}
	if err := validateDescriptors(items); err != nil {
		return nil, err
	}
	byName := make(map[string]Descriptor, len(items))
	for _, item := range items {
		byName[item.Name] = item
	}
	filtered := make([]Descriptor, 0, len(items))
	allowedNames := make([]string, 0, len(items))
	if allowlist == nil {
		filtered = append(filtered, items...)
		for _, item := range filtered {
			allowedNames = append(allowedNames, item.Name)
		}
	} else {
		seen := make(map[string]struct{}, len(allowlist))
		for _, name := range allowlist {
			if _, duplicate := seen[name]; duplicate {
				continue
			}
			seen[name] = struct{}{}
			item, ok := byName[name]
			if !ok {
				return nil, fmt.Errorf("%w: allowlisted %s", ErrNotFound, name)
			}
			filtered = append(filtered, item)
			allowedNames = append(allowedNames, name)
		}
	}
	sort.Slice(filtered, func(i, j int) bool { return filtered[i].Name < filtered[j].Name })
	if len(filtered) > options.MaxAdvertisedEntries {
		return nil, fmt.Errorf("%w: advertised catalog exceeds %d entries", ErrTooLarge, options.MaxAdvertisedEntries)
	}
	prompt := promptFor(filtered)
	if len(prompt) > options.MaxAdvertisedMetadataBytes {
		return nil, fmt.Errorf("%w: advertised metadata exceeds %d bytes", ErrTooLarge, options.MaxAdvertisedMetadataBytes)
	}
	frozen := make(memoryCatalog, len(filtered))
	for _, name := range allowedNames {
		content, err := catalog.Load(ctx, name)
		if err != nil {
			return nil, fmt.Errorf("snapshot skill %q: %w", name, err)
		}
		expected := byName[name]
		if !reflect.DeepEqual(content.Descriptor, expected) {
			return nil, fmt.Errorf("%w: descriptor changed while snapshotting %q", ErrInvalid, name)
		}
		if err := validateContent(content); err != nil {
			return nil, fmt.Errorf("snapshot skill %q: %w", name, err)
		}
		frozen[name] = cloneContent(content)
	}
	fingerprint, err := bundleFingerprint(filtered, frozen)
	if err != nil {
		return nil, err
	}
	return &Bundle{
		prompt:      prompt,
		tool:        LoadTool(frozen, nil),
		fingerprint: fingerprint,
	}, nil
}

func resolveOptions(configured []Options) (Options, error) {
	if len(configured) > 1 {
		return Options{}, fmt.Errorf("%w: at most one options value is allowed", ErrInvalid)
	}
	options := Options{}
	if len(configured) == 1 {
		options = configured[0]
	}
	if options.MaxAdvertisedEntries == 0 {
		options.MaxAdvertisedEntries = MaxAdvertisedEntries
	}
	if options.MaxAdvertisedMetadataBytes == 0 {
		options.MaxAdvertisedMetadataBytes = MaxAdvertisedMetadataBytes
	}
	if options.MaxAdvertisedEntries < 1 || options.MaxAdvertisedEntries > MaxAdvertisedEntries ||
		options.MaxAdvertisedMetadataBytes < 1 || options.MaxAdvertisedMetadataBytes > MaxAdvertisedMetadataBytes {
		return Options{}, fmt.Errorf("%w: limits must be positive and no greater than defaults", ErrInvalid)
	}
	return options, nil
}

// Fingerprint returns the stable SHA-256 identity of the bundle snapshot.
func (b *Bundle) Fingerprint() string {
	if b == nil {
		return ""
	}
	return b.fingerprint
}

func bundleFingerprint(descriptors []Descriptor, contents memoryCatalog) (string, error) {
	type fingerprintEntry struct {
		Name       string     `json:"name"`
		Descriptor Descriptor `json:"descriptor"`
		Digest     string     `json:"digest"`
		BodyDigest string     `json:"bodyDigest"`
		Provenance Provenance `json:"provenance"`
	}
	entries := make([]fingerprintEntry, 0, len(descriptors))
	for _, descriptor := range descriptors {
		content := contents[descriptor.Name]
		bodySum := sha256.Sum256([]byte(content.Body))
		entries = append(entries, fingerprintEntry{
			Name:       descriptor.Name,
			Descriptor: cloneDescriptor(descriptor),
			Digest:     content.Digest,
			BodyDigest: fmt.Sprintf("sha256:%x", bodySum),
			Provenance: content.Provenance,
		})
	}
	encoded, err := json.Marshal(entries)
	if err != nil {
		return "", fmt.Errorf("%w: fingerprint bundle: %v", ErrInvalid, err)
	}
	sum := sha256.Sum256(encoded)
	return fmt.Sprintf("sha256:%x", sum), nil
}

// CatalogPrompt returns the cached name-and-description catalog prompt.
func (b *Bundle) CatalogPrompt() string {
	if b == nil {
		return ""
	}
	return b.prompt
}

// LoadSkillTool returns the bundle's immutable load_skill tool.
func (b *Bundle) LoadSkillTool() tool.Tool {
	if b == nil {
		return nil
	}
	return b.tool
}

// Prompt returns a catalog summary containing only names and descriptions.
func Prompt(ctx context.Context, catalog Catalog) (string, error) {
	if catalog == nil {
		return "", fmt.Errorf("%w: nil catalog", ErrInvalid)
	}
	items, err := catalog.List(ctx)
	if err != nil {
		return "", err
	}
	if err := validateDescriptors(items); err != nil {
		return "", err
	}
	return promptFor(items), nil
}

func promptFor(items []Descriptor) string {
	var out strings.Builder
	out.WriteString("Available skills:\n")
	for _, item := range items {
		fmt.Fprintf(&out, "- %s: %s\n", item.Name, item.Description)
	}
	return out.String()
}

// LoadTool returns a load_skill tool. Complete instructions are disclosed only
// after a permitted skill is requested.
func LoadTool(catalog Catalog, allowlist []string) tool.Tool {
	allowed := make(map[string]struct{}, len(allowlist))
	for _, name := range allowlist {
		allowed[name] = struct{}{}
	}
	return &loadTool{catalog: catalog, allowed: allowed, restricted: allowlist != nil}
}

type loadTool struct {
	catalog    Catalog
	allowed    map[string]struct{}
	restricted bool
}

func (*loadTool) Name() string { return "load_skill" }
func (*loadTool) Description() string {
	return "Load the complete instructions for an available skill by name."
}
func (*loadTool) Schema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"name": map[string]any{"type": "string"},
		},
		"required": []string{"name"},
	}
}
func (t *loadTool) Call(ctx context.Context, input json.RawMessage, _ tool.Context) (tool.Result, error) {
	var request struct {
		Name string `json:"name"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(input)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return tool.Result{Error: tool.ErrInvalidArguments.Error(), ExitCode: 1},
			fmt.Errorf("%w: invalid load_skill input", tool.ErrInvalidArguments)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return tool.Result{Error: tool.ErrInvalidArguments.Error(), ExitCode: 1},
			fmt.Errorf("%w: trailing JSON input", tool.ErrInvalidArguments)
	}
	if !validSkillName(request.Name) {
		return unavailableResult()
	}
	if t.restricted {
		if _, ok := t.allowed[request.Name]; !ok {
			return unavailableResult()
		}
	}
	content, err := t.catalog.Load(ctx, request.Name)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return unavailableResult()
		}
		return tool.Result{Error: err.Error(), ExitCode: 1}, err
	}
	return tool.Result{
		Output: content.Body,
		Structured: map[string]any{
			"name":       content.Descriptor.Name,
			"digest":     content.Digest,
			"provenance": content.Provenance,
		},
	}, nil
}

func unavailableResult() (tool.Result, error) {
	return tool.Result{Error: ErrUnavailable.Error(), ExitCode: 1}, ErrUnavailable
}

type memoryCatalog map[string]Content

func (m memoryCatalog) List(context.Context) ([]Descriptor, error) {
	out := make([]Descriptor, 0, len(m))
	for _, content := range m {
		out = append(out, cloneDescriptor(content.Descriptor))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (m memoryCatalog) Load(_ context.Context, name string) (Content, error) {
	content, ok := m[name]
	if !ok {
		return Content{}, ErrNotFound
	}
	return cloneContent(content), nil
}
