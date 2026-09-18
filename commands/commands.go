// Package commands turns user-authored markdown files into canned tasks.
// A command is a prompt template with optional front matter, discovered from
// a directory, invoked as "/name args". The reference lets a command
// definition run shell inline unconditionally; here that is opt-in per
// definition and still goes through the shell tool's policy, so a checked-in
// command file cannot become an execution primitive on its own.
package commands

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// DefaultDir is where commands live relative to a workspace.
const DefaultDir = ".zenforge/commands"

// Defaults.
const (
	// DefaultMaxFileBytes bounds one command file.
	DefaultMaxFileBytes = 64 << 10
	// DefaultMaxIncludeBytes bounds an @file include.
	DefaultMaxIncludeBytes = 32 << 10
	// DefaultMaxArguments bounds the argument text a command may receive.
	DefaultMaxArguments = 8 << 10
)

// Layer names the catalog a command definition came from. The workspace layer
// is the project's own statement of what should happen, so it outranks the
// user layer when both define the same name: a checked-in /review means it,
// and a personal /review must not silently replace it.
type Layer string

const (
	// LayerWorkspace is the workspace's own catalog.
	LayerWorkspace Layer = "workspace"
	// LayerUser is the per-user catalog every workspace sees.
	LayerUser Layer = "user"
)

// Command is one canned task.
type Command struct {
	// Name is the invocation name, including any namespace prefix, e.g.
	// "git:commit" for a file at "git/commit.md".
	Name string
	// Description is shown in listings.
	Description string
	// ArgumentHint documents the arguments, e.g. "<path>".
	ArgumentHint string
	// Body is the template.
	Body string
	// Path is where the definition was read from.
	Path string
	// AllowBash permits inline !`cmd` execution in the body. It is false by
	// default so a command file alone cannot run anything.
	AllowBash bool
	// Model optionally overrides the model for this command.
	Model string
	// Agent optionally selects the subagent that should run this command.
	Agent string
	// AllowedTools optionally restricts the tools this command may use.
	AllowedTools []string
	// Layer is the catalog this definition came from. It is empty for a
	// catalog loaded by Load, which is not layered.
	Layer Layer
	// Shadowed marks a user definition a workspace definition of the same
	// name replaced. It stays in the catalog only so the listing can explain
	// why editing it changes nothing; it never resolves.
	Shadowed bool
}

// Catalog is a set of commands loaded from a directory.
type Catalog struct {
	commands map[string]Command
	// shadowed holds the user definitions a workspace definition replaced,
	// sorted by name. They are not resolvable; List shows them so the
	// shadowing is not silent.
	shadowed []Command
	// Dir is where the catalog was loaded from; empty for an empty catalog.
	Dir string
	// MaxIncludeBytes bounds @file includes.
	MaxIncludeBytes int
}

// frontMatterKeys are the keys this parser accepts. An unknown key is an
// error, because a silently ignored `allowed-tools` is a security surprise.
var frontMatterKeys = map[string]bool{
	"description":   true,
	"argument-hint": true,
	"model":         true,
	"agent":         true,
	"allowed-tools": true,
	"run-bash":      true,
}

// Load reads every *.md file under dir. A missing directory is an empty
// catalog, not an error: most repositories have no commands.
func Load(dir string) (*Catalog, error) {
	return load(dir, "")
}

// load is Load with the catalog's layer stamped on every definition. A
// non-empty layer also wraps every failure with the layer and the directory it
// happened in, so a malformed per-user command cannot be mistaken for a
// workspace problem.
func load(dir string, layer Layer) (*Catalog, error) {
	catalog := &Catalog{commands: map[string]Command{}, Dir: dir, MaxIncludeBytes: DefaultMaxIncludeBytes}
	if strings.TrimSpace(dir) == "" {
		return catalog, nil
	}
	root, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("resolve commands directory: %w", err)
	}
	info, err := os.Stat(root)
	if err != nil {
		if os.IsNotExist(err) {
			return catalog, nil
		}
		return nil, wrapLayerError(layer, root, fmt.Errorf("read commands directory: %w", err))
	}
	if !info.IsDir() {
		return nil, wrapLayerError(layer, root, fmt.Errorf("commands path %q is not a directory", dir))
	}
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if entry.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(strings.ToLower(entry.Name()), ".md") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read command %q: %w", path, err)
		}
		if len(raw) > DefaultMaxFileBytes {
			return fmt.Errorf("command %q is %d bytes, over the %d byte limit", path, len(raw), DefaultMaxFileBytes)
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return fmt.Errorf("resolve command name for %q: %w", path, err)
		}
		name := nameFromPath(relative)
		if name == "" {
			return fmt.Errorf("command %q has no usable name", path)
		}
		command, err := Parse(name, string(raw))
		if err != nil {
			return fmt.Errorf("command %s: %w", name, err)
		}
		command.Name = name
		command.Path = path
		command.Layer = layer
		if existing, ok := catalog.commands[name]; ok {
			return fmt.Errorf("command %q is defined twice: %s and %s", name, existing.Path, path)
		}
		catalog.commands[name] = command
		return nil
	})
	if err != nil {
		return nil, wrapLayerError(layer, root, err)
	}
	return catalog, nil
}

// LoadLayers loads the workspace and per-user command catalogs and merges
// them. A workspace definition wins over a user definition of the same name
// because the project's own definition is the more specific intent; the
// replaced user definition is retained so the listing can mark it. A missing
// directory on either side is an empty layer, not an error, so a machine with
// no per-user commands behaves exactly as it did before there was a user
// layer.
func LoadLayers(workspaceDir, userDir string) (*Catalog, error) {
	workspace, err := load(workspaceDir, LayerWorkspace)
	if err != nil {
		return nil, err
	}
	user, err := load(userDir, LayerUser)
	if err != nil {
		return nil, err
	}
	return merge(workspace, user), nil
}

// merge combines the two layers. The user layer is applied first and the
// workspace layer overrides it by name; each replaced user definition is kept
// (marked shadowed) so List can show that the user's edit was overridden
// rather than lost.
func merge(workspace, user *Catalog) *Catalog {
	merged := &Catalog{
		commands:        make(map[string]Command, user.Len()+workspace.Len()),
		Dir:             workspace.Dir,
		MaxIncludeBytes: DefaultMaxIncludeBytes,
	}
	if merged.Dir == "" {
		merged.Dir = user.Dir
	}
	for name, command := range user.commands {
		merged.commands[name] = command
	}
	for name, command := range workspace.commands {
		if existing, ok := user.commands[name]; ok {
			existing.Shadowed = true
			merged.shadowed = append(merged.shadowed, existing)
		}
		merged.commands[name] = command
	}
	sort.Slice(merged.shadowed, func(i, j int) bool { return merged.shadowed[i].Name < merged.shadowed[j].Name })
	return merged
}

// wrapLayerError prefixes a failure with the layer and directory it happened
// in. An unlayered Load (layer "") keeps the original error, so nothing
// changes for callers that never asked for layers.
func wrapLayerError(layer Layer, root string, err error) error {
	if layer == "" || err == nil {
		return err
	}
	return fmt.Errorf("%s commands directory %s: %w", layer, root, err)
}

// nameFromPath turns "git/commit.md" into "git:commit" and "review.md" into
// "review". Namespaces keep a directory of related commands readable while a
// flat name stays short.
func nameFromPath(relative string) string {
	trimmed := strings.TrimSuffix(relative, filepath.Ext(relative))
	parts := strings.Split(filepath.ToSlash(trimmed), "/")
	for _, part := range parts {
		if strings.TrimSpace(part) == "" {
			return ""
		}
	}
	return strings.Join(parts, ":")
}

// Parse reads a command definition: optional front matter, then the body.
func Parse(name, raw string) (Command, error) {
	command := Command{Name: name}
	body := strings.ReplaceAll(raw, "\r\n", "\n")
	if strings.HasPrefix(body, "---\n") || strings.TrimSpace(body) == "---" {
		rest := strings.TrimPrefix(body, "---\n")
		end := strings.Index(rest, "\n---")
		if end < 0 {
			return Command{}, fmt.Errorf("front matter is not closed with ---")
		}
		block := rest[:end]
		body = strings.TrimPrefix(rest[end+len("\n---"):], "\n")
		front, err := parseFrontMatter(block)
		if err != nil {
			return Command{}, err
		}
		command.Description = front["description"]
		command.ArgumentHint = front["argument-hint"]
		command.Model = front["model"]
		command.Agent = front["agent"]
		if raw, ok := front["run-bash"]; ok {
			allowed, err := strconv.ParseBool(strings.TrimSpace(raw))
			if err != nil {
				return Command{}, fmt.Errorf("run-bash must be true or false, got %q", raw)
			}
			command.AllowBash = allowed
		}
		if tools, ok := front["allowed-tools"]; ok {
			for _, tool := range strings.Split(tools, ",") {
				if trimmed := strings.TrimSpace(tool); trimmed != "" {
					command.AllowedTools = append(command.AllowedTools, trimmed)
				}
			}
		}
	}
	command.Body = strings.TrimSpace(body)
	if command.Body == "" {
		return Command{}, fmt.Errorf("command has an empty body")
	}
	return command, nil
}

// parseFrontMatter reads "key: value" lines. It is deliberately tiny: a
// command file is a prompt, not a configuration language, and a full YAML
// dependency would be a large surface for a small need.
func parseFrontMatter(block string) (map[string]string, error) {
	values := map[string]string{}
	for _, line := range strings.Split(block, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		key, value, ok := strings.Cut(trimmed, ":")
		if !ok {
			return nil, fmt.Errorf("front matter line %q is not key: value", trimmed)
		}
		key = strings.ToLower(strings.TrimSpace(key))
		if !frontMatterKeys[key] {
			return nil, fmt.Errorf("unknown front matter key %q", key)
		}
		values[key] = strings.Trim(strings.TrimSpace(value), `"'`)
	}
	return values, nil
}

// Get resolves a command name, accepting an optional leading slash and
// namespace separators written as either ":" or "/".
func (c *Catalog) Get(name string) (Command, bool) {
	if c == nil {
		return Command{}, false
	}
	key := strings.TrimPrefix(strings.TrimSpace(name), "/")
	key = strings.ReplaceAll(key, "/", ":")
	command, ok := c.commands[key]
	return command, ok
}

// Names returns the command names in sorted order.
func (c *Catalog) Names() []string {
	if c == nil {
		return nil
	}
	names := make([]string, 0, len(c.commands))
	for name := range c.commands {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Commands returns the catalog in sorted order.
func (c *Catalog) Commands() []Command {
	out := make([]Command, 0, len(c.Names()))
	for _, name := range c.Names() {
		out = append(out, c.commands[name])
	}
	return out
}

// Len reports how many commands the catalog holds.
func (c *Catalog) Len() int {
	if c == nil {
		return 0
	}
	return len(c.commands)
}

// Describe renders a listing line for the catalog.
func Describe(command Command) string {
	line := "/" + command.Name
	if command.ArgumentHint != "" {
		line += " " + command.ArgumentHint
	}
	if command.Description != "" {
		line += " - " + command.Description
	}
	return line
}

// describeWithSource adds the layer a definition came from, and marks a user
// definition a workspace one replaced. An unlayered catalog (Load) has no
// layer, so its lines keep the plain Describe form and existing callers are
// unaffected. The mark is what turns "my edit does nothing" into a visible
// reason instead of a mystery.
func describeWithSource(command Command) string {
	line := Describe(command)
	if command.Layer != "" {
		line += " [" + string(command.Layer) + "]"
	}
	if command.Shadowed {
		line += " (workspace overrides user)"
	}
	return line
}

// Shadowed returns the user definitions a workspace definition replaced, in
// name order. They are listed by List but never resolve.
func (c *Catalog) Shadowed() []Command {
	if c == nil {
		return nil
	}
	return append([]Command(nil), c.shadowed...)
}

// List renders the whole catalog, one command per line. A layered catalog
// also shows where each command came from and lists the user definitions a
// workspace definition replaced, directly under the winner, so shadowing is
// visible rather than silent.
func (c *Catalog) List() string {
	if c == nil || c.Len() == 0 {
		return ""
	}
	shadowed := make(map[string]Command, len(c.shadowed))
	for _, command := range c.shadowed {
		shadowed[command.Name] = command
	}
	lines := make([]string, 0, c.Len()+len(c.shadowed))
	for _, command := range c.Commands() {
		lines = append(lines, describeWithSource(command))
		if replaced, ok := shadowed[command.Name]; ok {
			lines = append(lines, describeWithSource(replaced))
		}
	}
	return strings.Join(lines, "\n")
}
