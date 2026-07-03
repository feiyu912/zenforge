// Package skill defines discovery and progressive-loading primitives for
// Agent Skills.
package skill

import (
	"context"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

const (
	// MaxNameBytes is the Agent Skills name limit.
	MaxNameBytes = 64
	// MaxDescriptionBytes is the default description limit.
	MaxDescriptionBytes = 512
	// MaxContentBytes is the default SKILL.md size limit.
	MaxContentBytes = 64 << 10
	// MaxFrontmatterBytes is the default YAML frontmatter size limit.
	MaxFrontmatterBytes = 8 << 10
	// MaxCatalogEntries is the maximum number of filesystem catalog entries.
	MaxCatalogEntries = 256
	// MaxAdvertisedEntries is the maximum number of skills advertised per bundle.
	MaxAdvertisedEntries = 64
	// MaxAdvertisedMetadataBytes is the maximum byte length of CatalogPrompt.
	MaxAdvertisedMetadataBytes = 32 << 10
)

var validName = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
var validDigest = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

var (
	// ErrNotFound indicates that a requested skill is not in a catalog.
	ErrNotFound = errors.New("skill not found")
	// ErrNotAllowed indicates that a requested skill is outside an allowlist.
	ErrNotAllowed = errors.New("skill not allowed")
	// ErrUnavailable is returned externally for unknown and denied skills alike.
	ErrUnavailable = errors.New("skill unavailable")
	// ErrInvalid indicates malformed skill metadata or content.
	ErrInvalid = errors.New("invalid skill")
	// ErrTooLarge indicates a configured content limit was exceeded.
	ErrTooLarge = errors.New("skill content too large")
	// ErrPathEscape indicates an unsafe path or symbolic-link traversal.
	ErrPathEscape = errors.New("skill path escapes source")
)

// Descriptor is the small, prompt-safe portion of a skill.
type Descriptor struct {
	Name          string         `json:"name"`
	Description   string         `json:"description"`
	License       string         `json:"license,omitempty"`
	Compatibility string         `json:"compatibility,omitempty"`
	Metadata      map[string]any `json:"metadata,omitempty"`
}

// Provenance identifies where loaded content came from.
type Provenance struct {
	Source string `json:"source"`
	Path   string `json:"path"`
}

// Content is a fully loaded skill. Body excludes YAML frontmatter. Digest is
// the SHA-256 digest of the original, complete SKILL.md bytes.
type Content struct {
	Descriptor Descriptor `json:"descriptor"`
	Body       string     `json:"instructions"`
	Digest     string     `json:"digest"`
	Provenance Provenance `json:"provenance"`
}

// Catalog discovers descriptors and loads complete skill instructions.
type Catalog interface {
	List(ctx context.Context) ([]Descriptor, error)
	Load(ctx context.Context, name string) (Content, error)
}

// Merge combines catalogs in priority order. Later catalogs override earlier
// catalogs when names collide.
func Merge(catalogs ...Catalog) Catalog {
	return &mergedCatalog{catalogs: append([]Catalog(nil), catalogs...)}
}

type mergedCatalog struct {
	catalogs []Catalog
}

func (m *mergedCatalog) List(ctx context.Context) ([]Descriptor, error) {
	byName := make(map[string]Descriptor)
	for _, catalog := range m.catalogs {
		if catalog == nil {
			continue
		}
		items, err := catalog.List(ctx)
		if err != nil {
			return nil, err
		}
		for _, item := range items {
			byName[item.Name] = cloneDescriptor(item)
		}
	}
	out := make([]Descriptor, 0, len(byName))
	for _, item := range byName {
		out = append(out, cloneDescriptor(item))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (m *mergedCatalog) Load(ctx context.Context, name string) (Content, error) {
	for i := len(m.catalogs) - 1; i >= 0; i-- {
		if m.catalogs[i] == nil {
			continue
		}
		content, err := m.catalogs[i].Load(ctx, name)
		if err == nil {
			return cloneContent(content), nil
		}
		if !errors.Is(err, ErrNotFound) {
			return Content{}, err
		}
	}
	return Content{}, fmt.Errorf("%w: %s", ErrNotFound, name)
}

func validSkillName(name string) bool {
	return len(name) > 0 && len(name) <= MaxNameBytes && validName.MatchString(name)
}

func validateDescriptors(items []Descriptor) error {
	if len(items) > MaxCatalogEntries {
		return fmt.Errorf("%w: catalog exceeds %d entries", ErrTooLarge, MaxCatalogEntries)
	}
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		if !validSkillName(item.Name) {
			return fmt.Errorf("%w: invalid skill name", ErrInvalid)
		}
		if _, duplicate := seen[item.Name]; duplicate {
			return fmt.Errorf("%w: duplicate skill name %q", ErrInvalid, item.Name)
		}
		seen[item.Name] = struct{}{}
		if len(item.Description) == 0 || len(item.Description) > MaxDescriptionBytes ||
			strings.ContainsAny(item.Description, "\r\n") || containsControl(item.Description) {
			return fmt.Errorf("%w: invalid description for %q", ErrInvalid, item.Name)
		}
	}
	return nil
}

func validateContent(content Content) error {
	if len(content.Body) > MaxContentBytes {
		return fmt.Errorf("%w: body exceeds %d bytes", ErrTooLarge, MaxContentBytes)
	}
	if !validDigest.MatchString(content.Digest) {
		return fmt.Errorf("%w: invalid digest", ErrInvalid)
	}
	if containsControl(content.Provenance.Source) {
		return fmt.Errorf("%w: invalid provenance source", ErrInvalid)
	}
	if content.Provenance.Path == "" || filepath.IsAbs(content.Provenance.Path) ||
		strings.Contains(content.Provenance.Path, `\`) ||
		path.Clean(content.Provenance.Path) != content.Provenance.Path ||
		filepath.Clean(content.Provenance.Path) != content.Provenance.Path ||
		content.Provenance.Path == ".." ||
		strings.HasPrefix(content.Provenance.Path, ".."+string(filepath.Separator)) {
		return fmt.Errorf("%w: invalid provenance path", ErrInvalid)
	}
	return nil
}

func containsControl(value string) bool {
	return strings.IndexFunc(value, unicode.IsControl) >= 0
}

func cloneDescriptor(in Descriptor) Descriptor {
	out := in
	out.Metadata = cloneMetadata(in.Metadata)
	return out
}

func cloneContent(in Content) Content {
	out := in
	out.Descriptor = cloneDescriptor(in.Descriptor)
	return out
}

func cloneMetadata(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	return cloneValue(reflect.ValueOf(in)).Interface().(map[string]any)
}

func cloneValue(in reflect.Value) reflect.Value {
	if !in.IsValid() {
		return in
	}
	switch in.Kind() {
	case reflect.Interface:
		if in.IsNil() {
			return reflect.Zero(in.Type())
		}
		value := cloneValue(in.Elem())
		out := reflect.New(in.Type()).Elem()
		out.Set(value)
		return out
	case reflect.Map:
		if in.IsNil() {
			return reflect.Zero(in.Type())
		}
		out := reflect.MakeMapWithSize(in.Type(), in.Len())
		iter := in.MapRange()
		for iter.Next() {
			out.SetMapIndex(cloneValue(iter.Key()), cloneValue(iter.Value()))
		}
		return out
	case reflect.Slice:
		if in.IsNil() {
			return reflect.Zero(in.Type())
		}
		out := reflect.MakeSlice(in.Type(), in.Len(), in.Len())
		for i := 0; i < in.Len(); i++ {
			out.Index(i).Set(cloneValue(in.Index(i)))
		}
		return out
	case reflect.Pointer:
		if in.IsNil() {
			return reflect.Zero(in.Type())
		}
		out := reflect.New(in.Type().Elem())
		out.Elem().Set(cloneValue(in.Elem()))
		return out
	default:
		return in
	}
}
