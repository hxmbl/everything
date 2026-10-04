// Optional chroma syntax highlighting for --color.

package main

import (
	"io"
	"path/filepath"
	"sync"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/formatters"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
)

// lexerCache memoizes lexers by file extension; Match is not cheap and the
// same handful of extensions recur across every file in a dump.
var lexerCache sync.Map

// emitHighlighted applies syntax highlighting to the file content and writes it to the writer.
// It uses chroma lexers and formatters to detect the language and apply the theme.
func emitHighlighted(w io.Writer, path string, data []byte, themeName string) error {
	lexer := matchLexer(path)
	iterator, err := lexer.Tokenise(nil, string(data))
	if err != nil {
		_, err = w.Write(data)
		return err
	}
	formatter := formatters.Get("terminal")
	if formatter == nil {
		formatter = formatters.Fallback
	}
	return formatter.Format(w, styles.Get(themeName), iterator)
}

// matchLexer finds the appropriate chroma lexer for a file based on its path.
// It caches lexers by file extension to avoid repeated lookups.
func matchLexer(path string) chroma.Lexer {
	ext := filepath.Ext(path)
	if v, ok := lexerCache.Load(ext); ok {
		return v.(chroma.Lexer)
	}
	lexer := lexers.Match(path)
	if lexer == nil {
		lexer = lexers.Fallback
	}
	lexer = chroma.Coalesce(lexer)
	lexerCache.Store(ext, lexer)
	return lexer
}
