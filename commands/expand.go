package commands

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Expand turns a command plus its arguments into the task text.
//
// Substitutions, all bounded:
//
//	$ARGUMENTS   the whole argument string
//	$1 .. $9     positional arguments (shell-like quoting)
//	@path        the file's contents, workspace-relative
//	`!cmd`       shell output, only when the command sets run-bash: true
//
// The expander is pure with respect to the workspace except for @file reads
// and the bash callback, both of which are injected, so the whole thing is
// testable without a shell or a filesystem.
func Expand(command Command, args string, options ExpandOptions) (string, error) {
	if strings.TrimSpace(args) != "" {
		if options.MaxArguments > 0 && len(args) > options.MaxArguments {
			return "", fmt.Errorf("arguments are %d bytes, over the %d byte limit", len(args), options.MaxArguments)
		}
	} else {
		args = ""
	}
	// Authoring features run first, argument substitution last. Two reasons:
	// a command's @include and !`shell` are properties of the definition, so
	// arguments must not be able to introduce them, and substituted text is
	// never re-scanned for further placeholders.
	body, err := expandIncludes(command.Body, options)
	if err != nil {
		return "", err
	}
	body, err = expandBash(body, command, options)
	if err != nil {
		return "", err
	}
	expanded := substituteArguments(body, args)
	if strings.TrimSpace(expanded) == "" {
		return "", fmt.Errorf("command /%s expanded to nothing", command.Name)
	}
	return expanded, nil
}

// ExpandOptions injects the expander's environment.
type ExpandOptions struct {
	// Workspace is the root @path includes resolve against.
	Workspace string
	// MaxIncludeBytes bounds one include. Zero uses DefaultMaxIncludeBytes.
	MaxIncludeBytes int
	// MaxIncludeFiles bounds how many includes one command may make.
	MaxIncludeFiles int
	// MaxArguments bounds the argument text. Zero uses DefaultMaxArguments.
	MaxArguments int
	// Bash runs an inline command. It is called only when the command sets
	// run-bash: true, and its output replaces the backtick expression.
	Bash func(command string) (string, error)
}

// DefaultMaxIncludeFiles bounds a single expansion's includes.
const DefaultMaxIncludeFiles = 20

// substituteArguments replaces $ARGUMENTS and $1..$9 in one pass, so text
// that arrives through an argument is never scanned again. A placeholder
// beyond the arguments given expands to nothing, and "$$" escapes a dollar.
func substituteArguments(body, args string) string {
	var (
		builder    strings.Builder
		positional = splitArguments(args)
	)
	for index := 0; index < len(body); {
		if body[index] != '$' {
			builder.WriteByte(body[index])
			index++
			continue
		}
		if index+1 < len(body) && body[index+1] == '$' {
			builder.WriteByte('$')
			index += 2
			continue
		}
		if strings.HasPrefix(body[index:], "$ARGUMENTS") {
			builder.WriteString(args)
			index += len("$ARGUMENTS")
			continue
		}
		if index+1 < len(body) && body[index+1] >= '1' && body[index+1] <= '9' {
			position := int(body[index+1] - '1')
			if position < len(positional) {
				builder.WriteString(positional[position])
			}
			index += 2
			continue
		}
		builder.WriteByte(body[index])
		index++
	}
	return builder.String()
}

// splitArguments splits a shell-like argument string, honouring single and
// double quotes so `$1` can carry a phrase.
func splitArguments(args string) []string {
	var (
		out     []string
		current strings.Builder
		quote   rune
		escaped bool
	)
	flush := func() {
		if current.Len() > 0 {
			out = append(out, current.String())
			current.Reset()
		}
	}
	for _, r := range args {
		switch {
		case escaped:
			current.WriteRune(r)
			escaped = false
		case r == '\\' && quote != '\'':
			escaped = true
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				current.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote = r
		case r == ' ' || r == '\t' || r == '\n':
			flush()
		default:
			current.WriteRune(r)
		}
	}
	flush()
	return out
}

// expandIncludes substitutes @path references with file contents. A reference
// that escapes the workspace is refused rather than read: a command file is
// not a licence to read arbitrary paths.
func expandIncludes(body string, options ExpandOptions) (string, error) {
	maxBytes := options.MaxIncludeBytes
	if maxBytes <= 0 {
		maxBytes = DefaultMaxIncludeBytes
	}
	maxFiles := options.MaxIncludeFiles
	if maxFiles <= 0 {
		maxFiles = DefaultMaxIncludeFiles
	}
	var (
		builder  strings.Builder
		includes int
	)
	for index := 0; index < len(body); {
		r := body[index]
		// An include must start at a word boundary, so an email address or
		// a handle in prose is left alone.
		if r != '@' || index+1 >= len(body) || !isIncludeStart(body[index+1]) || !atIncludeBoundary(body, index) {
			builder.WriteByte(r)
			index++
			continue
		}
		end := index + 1
		for end < len(body) && isIncludeChar(body[end]) {
			end++
		}
		reference := body[index+1 : end]
		includes++
		if includes > maxFiles {
			return "", fmt.Errorf("command includes more than %d files", maxFiles)
		}
		content, err := readInclude(reference, options.Workspace, maxBytes)
		if err != nil {
			return "", err
		}
		builder.WriteString(content)
		index = end
	}
	return builder.String(), nil
}

// atIncludeBoundary reports whether the "@" at index begins a reference
// rather than appearing inside a word.
func atIncludeBoundary(body string, index int) bool {
	if index == 0 {
		return true
	}
	switch body[index-1] {
	case ' ', '\t', '\n', '\r', '(', '[', '{', '"', '\'', '>', '-', '*', ',', ';', ':':
		return true
	default:
		return false
	}
}

func isIncludeStart(b byte) bool {
	return b == '.' || b == '/' || b == '_' || b == '-' ||
		(b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

func isIncludeChar(b byte) bool {
	return isIncludeStart(b) || b == ':' || b == '~'
}

// readInclude reads one workspace-relative file. The path is cleaned and
// confined: both ".." escapes and absolute paths are refused.
func readInclude(reference, workspace string, maxBytes int) (string, error) {
	if workspace == "" {
		return "", fmt.Errorf("cannot include %q: no workspace is configured", reference)
	}
	if filepath.IsAbs(reference) {
		return "", fmt.Errorf("cannot include %q: absolute paths are refused", reference)
	}
	root, err := filepath.Abs(workspace)
	if err != nil {
		return "", fmt.Errorf("resolve workspace: %w", err)
	}
	target := filepath.Join(root, filepath.FromSlash(reference))
	relative, err := filepath.Rel(root, target)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("cannot include %q: it is outside the workspace", reference)
	}
	info, err := os.Stat(target)
	if err != nil {
		return "", fmt.Errorf("cannot include %q: %w", reference, err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("cannot include %q: it is a directory", reference)
	}
	if info.Size() > int64(maxBytes) {
		return "", fmt.Errorf("cannot include %q: it is %d bytes, over the %d byte limit", reference, info.Size(), maxBytes)
	}
	raw, err := os.ReadFile(target)
	if err != nil {
		return "", fmt.Errorf("cannot include %q: %w", reference, err)
	}
	return string(raw), nil
}

// expandBash replaces !`cmd` (and !`cmd` inside the body) with the command's
// output, but only for a command that opted in. Without the opt-in the
// expression is left verbatim so the author sees it was not run, rather than
// the agent receiving a prompt with a silently missing section.
func expandBash(body string, command Command, options ExpandOptions) (string, error) {
	if !strings.Contains(body, "!`") {
		return body, nil
	}
	if !command.AllowBash {
		return body, nil
	}
	if options.Bash == nil {
		return "", fmt.Errorf("command /%s uses inline shell but no shell is available", command.Name)
	}
	var (
		builder strings.Builder
		runs    int
	)
	for index := 0; index < len(body); {
		if body[index] != '!' || index+1 >= len(body) || body[index+1] != '`' {
			builder.WriteByte(body[index])
			index++
			continue
		}
		end := strings.Index(body[index+2:], "`")
		if end < 0 {
			return "", fmt.Errorf("command /%s has an unterminated inline shell expression", command.Name)
		}
		expression := body[index+2 : index+2+end]
		runs++
		if runs > 10 {
			return "", fmt.Errorf("command /%s runs more than 10 inline shell expressions", command.Name)
		}
		output, err := options.Bash(expression)
		if err != nil {
			return "", fmt.Errorf("inline shell %q failed: %w", expression, err)
		}
		builder.WriteString(strings.TrimRight(output, "\n"))
		index = index + 2 + end + 1
	}
	return builder.String(), nil
}
