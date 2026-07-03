// Package fs implements Agent Skills catalogs backed by directories.
package fs

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	iofs "io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/feiyu912/zenforge/skill"
)

var validName = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

// Options controls filesystem catalog validation.
type Options struct {
	Source              string
	MaxContentBytes     int64
	MaxFrontmatterBytes int
	MaxDescriptionBytes int
	MaxCatalogEntries   int
}

// Catalog scans either a skill directory containing SKILL.md or a source
// directory whose immediate child directories contain SKILL.md files.
type Catalog struct {
	root    string
	options Options
}

// New constructs a filesystem catalog. Scanning occurs on List and Load so
// changes on disk are observed and validated on every operation.
func New(root string, options Options) (*Catalog, error) {
	if root == "" {
		return nil, fmt.Errorf("%w: empty root", skill.ErrInvalid)
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve skill root: %w", err)
	}
	if options.MaxContentBytes == 0 {
		options.MaxContentBytes = skill.MaxContentBytes
	}
	if options.MaxDescriptionBytes == 0 {
		options.MaxDescriptionBytes = skill.MaxDescriptionBytes
	}
	if options.MaxFrontmatterBytes == 0 {
		options.MaxFrontmatterBytes = skill.MaxFrontmatterBytes
	}
	if options.MaxCatalogEntries == 0 {
		options.MaxCatalogEntries = skill.MaxCatalogEntries
	}
	if options.MaxContentBytes < 1 || options.MaxContentBytes > skill.MaxContentBytes ||
		options.MaxFrontmatterBytes < 1 || options.MaxFrontmatterBytes > skill.MaxFrontmatterBytes ||
		options.MaxDescriptionBytes < 1 || options.MaxDescriptionBytes > skill.MaxDescriptionBytes ||
		options.MaxCatalogEntries < 1 || options.MaxCatalogEntries > skill.MaxCatalogEntries {
		return nil, fmt.Errorf("%w: limits must be positive and no greater than defaults", skill.ErrInvalid)
	}
	if options.Source == "" {
		options.Source = "filesystem"
	}
	return &Catalog{root: filepath.Clean(absolute), options: options}, nil
}

type entry struct {
	descriptor skill.Descriptor
	path       string
}

// List returns descriptors sorted by name.
func (c *Catalog) List(ctx context.Context) ([]skill.Descriptor, error) {
	entries, err := c.scan(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]skill.Descriptor, len(entries))
	for i := range entries {
		out[i] = cloneDescriptor(entries[i].descriptor)
	}
	return out, nil
}

// Load returns complete, validated content for name.
func (c *Catalog) Load(ctx context.Context, name string) (skill.Content, error) {
	entries, err := c.scan(ctx)
	if err != nil {
		return skill.Content{}, err
	}
	index := sort.Search(len(entries), func(i int) bool { return entries[i].descriptor.Name >= name })
	if index == len(entries) || entries[index].descriptor.Name != name {
		return skill.Content{}, fmt.Errorf("%w: %s", skill.ErrNotFound, name)
	}
	raw, err := c.readSafe(entries[index].path)
	if err != nil {
		return skill.Content{}, err
	}
	sum := sha256.Sum256(raw)
	descriptor, body, err := parseDocument(raw)
	if err != nil {
		return skill.Content{}, fmt.Errorf("%w: %s: %v", skill.ErrInvalid, entries[index].path, err)
	}
	if err := validateDescriptor(descriptor, filepath.Base(filepath.Dir(entries[index].path)), c.options.MaxDescriptionBytes); err != nil {
		return skill.Content{}, fmt.Errorf("%w: %s: %v", skill.ErrInvalid, entries[index].path, err)
	}
	provenancePath, err := filepath.Rel(c.root, entries[index].path)
	if err != nil || provenancePath == ".." || strings.HasPrefix(provenancePath, ".."+string(filepath.Separator)) {
		return skill.Content{}, fmt.Errorf("%w: provenance path %s", skill.ErrPathEscape, entries[index].path)
	}
	return skill.Content{
		Descriptor: cloneDescriptor(descriptor),
		Body:       body,
		Digest:     "sha256:" + hex.EncodeToString(sum[:]),
		Provenance: skill.Provenance{Source: c.options.Source, Path: filepath.ToSlash(provenancePath)},
	}, nil
}

func (c *Catalog) scan(ctx context.Context) ([]entry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	info, err := os.Lstat(c.root)
	if err != nil {
		return nil, fmt.Errorf("stat skill root: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%w: root is a symlink", skill.ErrPathEscape)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%w: root is not a directory", skill.ErrInvalid)
	}

	direct := filepath.Join(c.root, "SKILL.md")
	if _, err := os.Lstat(direct); err == nil {
		item, err := c.inspect(c.root)
		if err != nil {
			return nil, err
		}
		return []entry{item}, nil
	} else if !errors.Is(err, iofs.ErrNotExist) {
		return nil, fmt.Errorf("stat SKILL.md: %w", err)
	}

	children, err := os.ReadDir(c.root)
	if err != nil {
		return nil, fmt.Errorf("read skill source: %w", err)
	}
	var out []entry
	for _, child := range children {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if child.Type()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("%w: %s", skill.ErrPathEscape, child.Name())
		}
		if !child.IsDir() {
			continue
		}
		path := filepath.Join(c.root, child.Name(), "SKILL.md")
		if _, err := os.Lstat(path); errors.Is(err, iofs.ErrNotExist) {
			continue
		} else if err != nil {
			return nil, fmt.Errorf("stat %s: %w", path, err)
		}
		if len(out) >= c.options.MaxCatalogEntries {
			return nil, fmt.Errorf("%w: catalog exceeds %d entries", skill.ErrTooLarge, c.options.MaxCatalogEntries)
		}
		item, err := c.inspect(filepath.Join(c.root, child.Name()))
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].descriptor.Name < out[j].descriptor.Name })
	return out, nil
}

func (c *Catalog) inspect(dir string) (entry, error) {
	path := filepath.Join(dir, "SKILL.md")
	raw, err := c.readFrontmatterSafe(path)
	if err != nil {
		return entry{}, err
	}
	descriptor, err := parseFrontmatter(raw)
	if err != nil {
		return entry{}, fmt.Errorf("%w: %s: %v", skill.ErrInvalid, path, err)
	}
	if err := validateDescriptor(descriptor, filepath.Base(dir), c.options.MaxDescriptionBytes); err != nil {
		return entry{}, fmt.Errorf("%w: %s: %v", skill.ErrInvalid, path, err)
	}
	return entry{descriptor: descriptor, path: path}, nil
}

func (c *Catalog) readFrontmatterSafe(path string) ([]byte, error) {
	file, err := c.openSafe(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	reader := bufio.NewReader(io.LimitReader(file, c.options.MaxContentBytes+1))
	var raw strings.Builder
	delimiters := 0
	for {
		line, readErr := reader.ReadString('\n')
		raw.WriteString(line)
		if strings.TrimSpace(line) == "---" {
			delimiters++
			if delimiters == 2 {
				return []byte(raw.String()), nil
			}
		}
		if raw.Len() > c.options.MaxFrontmatterBytes {
			return nil, fmt.Errorf("%w: frontmatter exceeds %d bytes", skill.ErrInvalid, c.options.MaxFrontmatterBytes)
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return []byte(raw.String()), nil
			}
			return nil, fmt.Errorf("read skill frontmatter: %w", readErr)
		}
	}
}

func (c *Catalog) readSafe(path string) ([]byte, error) {
	file, err := c.openSafe(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, c.options.MaxContentBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read skill content: %w", err)
	}
	if int64(len(raw)) > c.options.MaxContentBytes {
		return nil, fmt.Errorf("%w: %s", skill.ErrTooLarge, path)
	}
	return raw, nil
}

func (c *Catalog) openSafe(path string) (*os.File, error) {
	clean := filepath.Clean(path)
	relative, err := filepath.Rel(c.root, clean)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("%w: %s", skill.ErrPathEscape, path)
	}
	info, err := os.Lstat(clean)
	if err != nil {
		return nil, fmt.Errorf("stat skill content: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: content must be a regular non-symlink file", skill.ErrPathEscape)
	}
	if info.Size() > c.options.MaxContentBytes {
		return nil, fmt.Errorf("%w: %s", skill.ErrTooLarge, path)
	}
	file, err := os.Open(clean)
	if err != nil {
		return nil, fmt.Errorf("open skill content: %w", err)
	}
	openedInfo, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, fmt.Errorf("stat opened skill content: %w", err)
	}
	if !os.SameFile(info, openedInfo) {
		file.Close()
		return nil, fmt.Errorf("%w: content changed while opening", skill.ErrPathEscape)
	}
	return file, nil
}

func validateDescriptor(item skill.Descriptor, directory string, maxDescription int) error {
	if len(item.Name) == 0 || len(item.Name) > skill.MaxNameBytes || !validName.MatchString(item.Name) {
		return fmt.Errorf("name %q must use lowercase alphanumerics and single hyphens (max %d bytes)", item.Name, skill.MaxNameBytes)
	}
	if item.Name != directory {
		return fmt.Errorf("name %q does not match directory %q", item.Name, directory)
	}
	if strings.TrimSpace(item.Description) == "" {
		return errors.New("description is required")
	}
	if len(item.Description) > maxDescription {
		return fmt.Errorf("description exceeds %d bytes", maxDescription)
	}
	return nil
}

func parseFrontmatter(raw []byte) (skill.Descriptor, error) {
	scanner := bufio.NewScanner(strings.NewReader(string(raw)))
	if !scanner.Scan() || strings.TrimSpace(scanner.Text()) != "---" {
		return skill.Descriptor{}, errors.New("missing opening frontmatter delimiter")
	}
	values := make(map[string]string)
	metadata := make(map[string]any)
	seen := make(map[string]struct{})
	metadataSeen := make(map[string]struct{})
	inMetadata := false
	closed := false
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "---" {
			closed = true
			break
		}
		if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " "))
		key, value, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok || strings.TrimSpace(key) == "" {
			return skill.Descriptor{}, fmt.Errorf("invalid frontmatter line %q", line)
		}
		key, value = strings.TrimSpace(key), unquote(strings.TrimSpace(value))
		if indent > 0 {
			if !inMetadata {
				return skill.Descriptor{}, fmt.Errorf("nested value outside metadata: %q", line)
			}
			if _, duplicate := metadataSeen[key]; duplicate {
				return skill.Descriptor{}, fmt.Errorf("duplicate metadata key %q", key)
			}
			metadataSeen[key] = struct{}{}
			if hasControl(value) {
				return skill.Descriptor{}, fmt.Errorf("control character in metadata %q", key)
			}
			metadata[key] = value
			continue
		}
		if _, duplicate := seen[key]; duplicate {
			return skill.Descriptor{}, fmt.Errorf("duplicate frontmatter key %q", key)
		}
		seen[key] = struct{}{}
		inMetadata = key == "metadata" && value == ""
		if hasControl(value) {
			return skill.Descriptor{}, fmt.Errorf("control character in %q", key)
		}
		switch key {
		case "name", "license", "compatibility":
			values[key] = value
		case "description":
			if value == "|" || value == ">" || value == "|-" || value == ">-" || value == "|+" || value == ">+" {
				return skill.Descriptor{}, errors.New("multiline description is not supported")
			}
			values[key] = value
		case "metadata":
			if value != "" {
				return skill.Descriptor{}, errors.New("metadata must be a mapping")
			}
		default:
			return skill.Descriptor{}, fmt.Errorf("unknown frontmatter field %q", key)
		}
	}
	if err := scanner.Err(); err != nil {
		return skill.Descriptor{}, err
	}
	if !closed {
		return skill.Descriptor{}, errors.New("missing closing frontmatter delimiter")
	}
	if len(metadata) == 0 {
		metadata = nil
	}
	return skill.Descriptor{
		Name: values["name"], Description: values["description"],
		License: values["license"], Compatibility: values["compatibility"], Metadata: metadata,
	}, nil
}

func parseDocument(raw []byte) (skill.Descriptor, string, error) {
	descriptor, err := parseFrontmatter(raw)
	if err != nil {
		return skill.Descriptor{}, "", err
	}
	offset, err := bodyOffset(raw)
	if err != nil {
		return skill.Descriptor{}, "", err
	}
	return descriptor, string(raw[offset:]), nil
}

func bodyOffset(raw []byte) (int, error) {
	offset, delimiters := 0, 0
	for offset < len(raw) {
		next := strings.IndexByte(string(raw[offset:]), '\n')
		end := len(raw)
		if next >= 0 {
			end = offset + next + 1
		}
		if strings.TrimSpace(string(raw[offset:end])) == "---" {
			delimiters++
			if delimiters == 2 {
				return end, nil
			}
		}
		offset = end
	}
	return 0, errors.New("missing closing frontmatter delimiter")
}

func hasControl(value string) bool {
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

func cloneDescriptor(in skill.Descriptor) skill.Descriptor {
	out := in
	if in.Metadata != nil {
		out.Metadata = make(map[string]any, len(in.Metadata))
		for key, value := range in.Metadata {
			out.Metadata[key] = value
		}
	}
	return out
}

func unquote(value string) string {
	if len(value) >= 2 && ((value[0] == '"' && value[len(value)-1] == '"') ||
		(value[0] == '\'' && value[len(value)-1] == '\'')) {
		return value[1 : len(value)-1]
	}
	return value
}
