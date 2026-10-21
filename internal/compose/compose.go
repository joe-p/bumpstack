// Package compose is a line-oriented reader/writer for docker-compose files.
//
// The file is edited in place line by line rather than parsed and re-serialised
// so that comments, ordering, quoting and formatting are all preserved. An
// image is a candidate for bumping when the mapping is preceded by a comment of
// the form `# version: <regex>`, e.g.
//
//	services:
//	  gluetun:
//	    # version: ^v3.\d+$
//	    image: qmcgaw/gluetun:v3.41@sha256:...
package compose

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"unicode"

	"github.com/joe-p/bumpstack/internal/reference"
)

var (
	versionCommentRE = regexp.MustCompile(`^\s*#\s*version\s*:\s*(.+?)\s*$`)
	mappingRE        = regexp.MustCompile(`^(\s*)([A-Za-z0-9_.\-]+)\s*:\s*(.*)$`)
	blockStartRE     = regexp.MustCompile(`^(\s*)(?:(-\s+)?(?:[A-Za-z0-9_.\-]+|"[^"]*"|'[^']*')\s*:\s*|-\s+)(?:&[^\s]+\s+)?([|>](?:[1-9][+-]?|[+-][1-9]?)?\s*(?:#.*)?)$`)
)

// Image is an `image:` mapping that carries a `# version:` pattern.
type Image struct {
	// Line is the zero-based index into the file's lines.
	Line int
	// Service is the nearest enclosing service key, or "" when unknown.
	Service string
	// Original is the original, unquoted value.
	Original string
	// Reference is the parsed reference.
	Reference reference.Reference
	// VersionPattern is the raw `# version:` pattern text.
	VersionPattern string
	// Pattern is the compiled pattern.
	Pattern *regexp.Regexp

	prefix string
	quote  byte
	suffix string
}

// RenderLine builds the replacement line for value, preserving the original
// prefix, quoting and trailing comment.
func (i *Image) RenderLine(value string) string {
	var b strings.Builder
	b.Grow(len(i.prefix) + len(value) + 4)
	b.WriteString(i.prefix)
	if i.quote != 0 {
		b.WriteByte(i.quote)
	}
	b.WriteString(value)
	if i.quote != 0 {
		b.WriteByte(i.quote)
	}
	b.WriteString(i.suffix)
	return b.String()
}

// PlainImage is an `image:` mapping with no accompanying `# version:` pattern.
type PlainImage struct {
	Line    int
	Service string
	Value   string
}

// File is a parsed compose file together with the images found in it.
type File struct {
	lines           []string
	newline         string
	trailingNewline bool
	// Images are the mappings that carry a version pattern.
	Images []*Image
	// Skipped are the mappings that were left alone for lack of a pattern.
	Skipped []PlainImage
}

type scalarParts struct {
	prefix string
	value  string
	quote  byte
	suffix string
}

// splitScalar splits a `key: value # comment` mapping line into its parts,
// honouring quotes.
func splitScalar(line string) (scalarParts, bool) {
	colon := strings.IndexByte(line, ':')
	if colon < 0 {
		return scalarParts{}, false
	}
	after := line[colon+1:]

	leading := len(after) - len(strings.TrimLeftFunc(after, unicode.IsSpace))
	prefix := line[:colon] + ":" + after[:leading]

	rest := after[leading:]
	body := strings.TrimRightFunc(rest, unicode.IsSpace)
	trailing := rest[len(body):]

	var quote byte
	var value, comment string

	switch {
	case len(body) > 0 && (body[0] == '"' || body[0] == '\''):
		quote = body[0]
		if close := strings.IndexByte(body[1:], quote); close >= 0 {
			value = body[1 : 1+close]
			if afterQuote := strings.TrimSpace(body[1+close+1:]); strings.HasPrefix(afterQuote, "#") {
				comment = afterQuote
			}
		} else {
			value = body[1:]
		}
	default:
		if pos := commentStart(body); pos >= 0 {
			value = strings.TrimRightFunc(body[:pos], unicode.IsSpace)
			comment = body[pos:]
		} else {
			value = body
		}
	}

	var suffix string
	if comment != "" {
		suffix = " " + comment
	}
	suffix += trailing

	return scalarParts{prefix: prefix, value: value, quote: quote, suffix: suffix}, true
}

// commentStart returns the index of a `#` that begins a YAML comment, i.e. one
// preceded by whitespace.
func commentStart(s string) int {
	for i := 1; i < len(s); i++ {
		if s[i] == '#' && (s[i-1] == ' ' || s[i-1] == '\t') {
			return i
		}
	}
	return -1
}

// Parse reads text, locating every bumpable image.
func Parse(text string) (*File, error) {
	newline := "\n"
	if strings.Contains(text, "\r\n") {
		newline = "\r\n"
	}
	trailingNewline := strings.HasSuffix(text, "\n")

	raw := strings.Split(text, "\n")
	if trailingNewline {
		raw = raw[:len(raw)-1]
	}
	lines := make([]string, len(raw))
	for i, line := range raw {
		lines[i] = strings.TrimSuffix(line, "\r")
	}

	file := &File{lines: lines, newline: newline, trailingNewline: trailingNewline}

	type frame struct {
		indent int
		key    string
	}
	var stack []frame
	currentService := ""
	pendingVersion := ""
	blockParentIndent, blockContentIndent := -1, -1

	for idx, rawLine := range lines {
		trimmed := strings.TrimSpace(rawLine)
		lineIndent := len(rawLine) - len(strings.TrimLeft(rawLine, " \t"))
		if blockParentIndent >= 0 {
			if trimmed == "" {
				continue
			}
			if blockContentIndent < 0 && lineIndent > blockParentIndent {
				blockContentIndent = lineIndent
			}
			if blockContentIndent >= 0 && lineIndent >= blockContentIndent {
				continue
			}
			blockParentIndent, blockContentIndent = -1, -1
		}

		if trimmed == "" {
			pendingVersion = ""
			continue
		}
		if strings.HasPrefix(trimmed, "#") {
			if m := versionCommentRE.FindStringSubmatch(rawLine); m != nil {
				if pattern := strings.TrimSpace(m[1]); pattern != "" {
					pendingVersion = pattern
				}
			}
			continue
		}

		// Block scalar contents are text, even when they look like mappings or
		// version comments. Recognize both mapping and sequence headers.
		if block := blockStartRE.FindStringSubmatch(rawLine); block != nil {
			blockParentIndent = lineIndent
			// In a sequence mapping, the key starts after the sequence dash.
			contentBase := lineIndent + len(block[2])
			for _, ch := range strings.Fields(block[3])[0] {
				if ch >= '1' && ch <= '9' {
					blockContentIndent = contentBase + int(ch-'0')
				}
			}
			pendingVersion = ""
			continue
		}
		m := mappingRE.FindStringSubmatch(rawLine)
		if m == nil {
			pendingVersion = ""
			continue
		}
		indent := len(m[1])
		key := m[2]
		isImage := key == "image" && strings.TrimSpace(m[3]) != ""

		// Track the enclosing key hierarchy to recover the service name.
		for len(stack) > 0 && stack[len(stack)-1].indent >= indent {
			stack = stack[:len(stack)-1]
		}
		if len(stack) == 0 {
			currentService = ""
		} else if len(stack) == 1 && stack[0].key == "services" {
			currentService = key
		}

		if isImage {
			if pendingVersion == "" {
				if parts, ok := splitScalar(rawLine); ok {
					file.Skipped = append(file.Skipped, PlainImage{
						Line:    idx,
						Service: currentService,
						Value:   parts.value,
					})
				}
				stack = append(stack, frame{indent, key})
				continue
			}

			patternText := pendingVersion
			pendingVersion = ""

			parts, ok := splitScalar(rawLine)
			if !ok {
				return nil, fmt.Errorf("malformed image mapping")
			}
			pattern, err := regexp.Compile(patternText)
			if err != nil {
				return nil, fmt.Errorf("line %d: invalid version pattern `%s`", idx+1, patternText)
			}
			ref, err := reference.Parse(parts.value)
			if err != nil {
				fmt.Fprintf(os.Stderr, "warning: line %d: %s\n", idx+1, err)
				stack = append(stack, frame{indent, key})
				continue
			}

			file.Images = append(file.Images, &Image{
				Line:           idx,
				Service:        currentService,
				Original:       parts.value,
				Reference:      ref,
				VersionPattern: patternText,
				Pattern:        pattern,
				prefix:         parts.prefix,
				quote:          parts.quote,
				suffix:         parts.suffix,
			})
		} else {
			pendingVersion = ""
		}

		stack = append(stack, frame{indent, key})
	}

	return file, nil
}

// Rewrite replaces the value on line with value, keeping formatting intact.
func (f *File) Rewrite(line int, value string) {
	for _, image := range f.Images {
		if image.Line == line {
			f.lines[line] = image.RenderLine(value)
			return
		}
	}
}

// Render serialises the file back to text.
func (f *File) Render() string {
	out := strings.Join(f.lines, f.newline)
	if f.trailingNewline {
		out += f.newline
	}
	return out
}
