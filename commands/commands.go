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
}

// Catalog is a set of commands loaded from a directory.
type Catalog struct {
	commands map[string]Command
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
		return nil, fmt.Errorf("read commands directory: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("commands path %q is not a directory", dir)
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
		if existing, ok := catalog.commands[name]; ok {
			return fmt.Errorf("command %q is defined twice: %s and %s", name, existing.Path, path)
		}
		catalog.commands[name] = command
		return nil
	})
	if err != nil {
		return nil, err
	}
	return catalog, nil
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

// List renders the whole catalog, one command per line.
func (c *Catalog) List() string {
	if c == nil || c.Len() == 0 {
		return ""
	}
	lines := make([]string, 0, c.Len())
	for _, command := range c.Commands() {
		lines = append(lines, Describe(command))
	}
	return strings.Join(lines, "\n")
}
