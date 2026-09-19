package cli

import (
	"strings"

	"github.com/feiyu912/zenforge/commands"
	"github.com/feiyu912/zenforge/internal/dshapi"
)

// consoleCommands adapts the host's command catalog to the console's commands
// namespace (ADR 0090).
//
// Both halves go through the code the command line already uses: the menu lists
// the same catalog `zenforge run` resolves, and a submitted line expands with the
// host's own `commands.Expand`, so a command's arguments, `@file` includes and
// shell permission follow the rules this host already enforces rather than a
// second interpretation written for the console.
type consoleCommands struct {
	catalog *commands.Catalog
	opts    options
}

// Commands lists the catalog for the composer's slash menu.
func (c consoleCommands) Commands() []dshapi.CommandDescriptor {
	listed := c.catalog.Commands()
	out := make([]dshapi.CommandDescriptor, 0, len(listed))
	for _, command := range listed {
		// The menu needs a description for every row, and a command file may
		// declare none; the catalog's own listing line is then the honest
		// description, because it says what the template's arguments are.
		description := strings.TrimSpace(command.Description)
		if description == "" {
			description = commands.Describe(command)
		}
		descriptor := dshapi.CommandDescriptor{Name: command.Name, Description: description}
		if hint := strings.TrimSpace(command.ArgumentHint); hint != "" {
			descriptor.Input = &dshapi.CommandInputDescriptor{Hint: hint}
		}
		out = append(out, descriptor)
	}
	return out
}

// Expand resolves one submitted line into the task text it stands for. A line
// that names no command -- or that only looks like one, such as a path -- reports
// false, which is what the composer renders as "unknown or malformed command".
func (c consoleCommands) Expand(line string) (string, string, bool) {
	name, ok := consoleCommandName(line)
	if !ok {
		return "", "", false
	}
	if _, found := c.catalog.Get(name); !found {
		return "", "", false
	}
	expanded, err := resolveCommand(c.catalog, line, c.opts)
	if err != nil {
		// The name resolved to a command but the expansion failed (a malformed
		// argument, an include that cannot be read). Reporting bad-line rather
		// than a success the operator cannot see the effect of.
		return "", "", false
	}
	return name, expanded, true
}

// consoleCommandName extracts the invocation name from "/name args", applying the
// same shape check the command line uses: a name has no path separators or dots,
// so "/usr/local/bin/thing" is not a command invocation.
func consoleCommandName(line string) (string, bool) {
	trimmed := strings.TrimLeft(line, " \t\n")
	if !strings.HasPrefix(trimmed, "/") {
		return "", false
	}
	name, _, _ := strings.Cut(strings.TrimPrefix(trimmed, "/"), " ")
	name = strings.TrimSpace(name)
	if name == "" || !looksLikeCommandName(name) {
		return "", false
	}
	return name, true
}
