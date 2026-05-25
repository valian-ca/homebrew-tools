// Package highlight wraps chroma for per-line syntax highlighting and
// computes the unified line diff rendered in the Detail screen.
package highlight

import (
	"strings"

	"github.com/alecthomas/chroma/v2/formatters"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
)

// Code returns src with ANSI syntax highlighting for the given chroma lexer
// name. When noColor is true, or the language is unknown, it returns src
// unchanged so the diff still renders as readable plain text.
func Code(src, language string, noColor bool) string {
	if noColor || src == "" {
		return src
	}
	lexer := lexers.Get(language)
	if lexer == nil {
		return src
	}
	formatter := formatters.Get("terminal256")
	if formatter == nil {
		return src
	}
	iterator, err := lexer.Tokenise(nil, src)
	if err != nil {
		return src
	}
	var buf strings.Builder
	if err := formatter.Format(&buf, styles.Get("native"), iterator); err != nil {
		return src
	}
	return strings.TrimSuffix(buf.String(), "\n")
}
