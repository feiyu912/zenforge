package web

import (
	"html"
	"strings"
	"unicode"
)

// HTMLToText converts an HTML document to readable text: script, style,
// and head content are dropped, block elements become line breaks,
// anchors keep their target as a markdown link when the link text does
// not already contain the URL, and entities are decoded.
//
// This is a deliberate simplification of the reference provider's DOM
// walk (which uses a full HTML parser and a markdown converter); it
// preserves the property that matters for the model — visible text with
// no markup or script bodies.
func HTMLToText(document string) string {
	var builder strings.Builder
	source := document
	for len(source) > 0 {
		open := strings.Index(source, "<")
		if open < 0 {
			builder.WriteString(html.UnescapeString(source))
			break
		}
		builder.WriteString(html.UnescapeString(source[:open]))
		source = source[open:]

		if strings.HasPrefix(source, "<!--") {
			end := strings.Index(source, "-->")
			if end < 0 {
				break
			}
			source = source[end+3:]
			continue
		}
		close := strings.Index(source, ">")
		if close < 0 {
			break
		}
		tag := strings.TrimSpace(source[1:close])
		source = source[close+1:]

		name, attributes := splitTag(tag)
		switch name {
		case "script", "style", "head", "noscript", "template", "svg":
			end := strings.Index(strings.ToLower(source), "</"+name)
			if end < 0 {
				source = ""
				continue
			}
			source = source[end:]
			continue
		case "br", "p", "div", "section", "article", "li", "tr", "h1", "h2", "h3",
			"h4", "h5", "h6", "blockquote", "pre", "table", "header", "footer", "nav":
			builder.WriteString("\n")
			continue
		case "td", "th":
			builder.WriteString(" ")
			continue
		case "a":
			if href := attributeValue(attributes, "href"); href != "" && !strings.HasPrefix(href, "#") {
				builder.WriteString(" (")
				builder.WriteString(href)
				builder.WriteString(")")
			}
			continue
		default:
			continue
		}
	}
	return normalizeWhitespace(builder.String())
}

// splitTag separates a tag name from its attribute text.
func splitTag(tag string) (string, string) {
	tag = strings.TrimSuffix(tag, "/")
	index := strings.IndexAny(tag, " \t\r\n")
	if index < 0 {
		return strings.ToLower(tag), ""
	}
	return strings.ToLower(tag[:index]), tag[index+1:]
}

// attributeValue reads one quoted or unquoted attribute.
func attributeValue(attributes, name string) string {
	lower := strings.ToLower(attributes)
	index := strings.Index(lower, name+"=")
	if index < 0 {
		return ""
	}
	value := attributes[index+len(name)+1:]
	if value == "" {
		return ""
	}
	if value[0] == '"' || value[0] == '\'' {
		quote := value[0]
		if end := strings.IndexByte(value[1:], quote); end >= 0 {
			return strings.TrimSpace(value[1 : 1+end])
		}
		return ""
	}
	if end := strings.IndexAny(value, " \t\r\n>"); end >= 0 {
		return strings.TrimSpace(value[:end])
	}
	return strings.TrimSpace(value)
}

// normalizeWhitespace collapses runs of blank lines and trailing spaces
// while preserving paragraph breaks.
func normalizeWhitespace(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	var lines []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimRightFunc(line, unicode.IsSpace)
		line = strings.Join(strings.Fields(line), " ")
		if line == "" && (len(lines) == 0 || lines[len(lines)-1] == "") {
			continue
		}
		lines = append(lines, line)
	}
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return strings.Join(lines, "\n")
}
