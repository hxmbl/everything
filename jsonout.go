// JSON and JSON Lines emission. jsonEscaper exists because encoding/json
// cannot stream a string it has not already buffered whole, and this tool
// streams file contents of arbitrary size.

package main

import (
	"encoding/json"
	"io"
	"unicode/utf8"
)

const hexDigits = "0123456789abcdef"

// jsonEscaper is a custom JSON string escaper that handles UTF-8 encoding correctly.
// It ensures that multi-byte UTF-8 sequences are not split across escape boundaries.
type jsonEscaper struct {
	dst      io.Writer
	pending  [utf8.UTFMax]byte
	nPending int
	err      error
}

// utf8SeqLen returns the expected length of a UTF-8 sequence starting with byte b.
// Returns 0 if b is not a valid UTF-8 sequence start byte.
func utf8SeqLen(b byte) int {
	switch {
	case b&0xE0 == 0xC0:
		return 2
	case b&0xF0 == 0xE0:
		return 3
	case b&0xF8 == 0xF0:
		return 4
	default:
		return 0
	}
}

func (e *jsonEscaper) writeByteEscape(b byte) {
	var esc [6]byte
	esc[0], esc[1], esc[2], esc[3] = '\\', 'u', '0', '0'
	esc[4], esc[5] = hexDigits[b>>4], hexDigits[b&0xf]
	e.emitRaw(esc[:])
}

func (e *jsonEscaper) emitRaw(p []byte) {
	if e.err != nil || len(p) == 0 {
		return
	}
	_, e.err = e.dst.Write(p)
}

func (e *jsonEscaper) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, e.err
	}
	total := len(p)
	data := p
	if e.nPending > 0 {
		data = make([]byte, 0, e.nPending+len(p))
		data = append(data, e.pending[:e.nPending]...)
		data = append(data, p...)
	}
	e.nPending = 0

	holdFrom := len(data)
	if n := len(data); n > 0 {
		limit := n - utf8.UTFMax
		if limit < 0 {
			limit = 0
		}
		for i := n - 1; i >= limit; i-- {
			b := data[i]
			if b < 0x80 {
				break
			}
			if b&0xC0 == 0x80 {
				continue
			}
			if need := utf8SeqLen(b); need > 0 && i+need > n {
				holdFrom = i
			}
			break
		}
	}

	start := 0
	flushTo := func(end int) {
		if end > start {
			e.emitRaw(data[start:end])
			start = end
		}
	}

	i := 0
	for i < holdFrom && e.err == nil {
		b := data[i]
		if b >= 0x80 {
			r, size := utf8.DecodeRune(data[i:])
			if r == utf8.RuneError && size == 1 {
				flushTo(i)
				e.emitRaw([]byte(`\ufffd`))
				i++
				start = i
				continue
			}
			i += size
			continue
		}

		var esc []byte
		switch b {
		case '"':
			esc = []byte(`\"`)
		case '\\':
			esc = []byte(`\\`)
		case '\n':
			esc = []byte(`\n`)
		case '\r':
			esc = []byte(`\r`)
		case '\t':
			esc = []byte(`\t`)
		}
		if esc != nil {
			flushTo(i)
			e.emitRaw(esc)
			i++
			start = i
			continue
		}
		if b < 0x20 {
			flushTo(i)
			e.writeByteEscape(b)
			i++
			start = i
			continue
		}
		i++
	}
	if e.err == nil {
		flushTo(holdFrom)
		e.nPending = copy(e.pending[:], data[holdFrom:])
	} else {
		e.nPending = 0
	}

	return total, e.err
}

func (e *jsonEscaper) Close() error {
	if e.nPending > 0 {
		e.nPending = 0
		e.emitRaw([]byte(`\ufffd`))
	}
	return e.err
}

// writeJSONString writes a byte slice as a JSON string to the writer.
// It handles UTF-8 encoding and proper JSON escaping.
func writeJSONString(w io.Writer, s []byte) {
	e := &jsonEscaper{dst: w}
	e.Write(s)
	e.Close()
}

// writeJSONLine writes a single JSON line record with path and content.
// It returns an error if writing fails.
func writeJSONLine(w io.Writer, path string, head []byte, rest io.Reader) error {
	if err := writeJSONRecord(w, path, head, rest); err != nil {
		return err
	}
	_, err := w.Write([]byte("\n"))
	return err
}

// writeJSONRecord writes a JSON record with path and content fields.
// It handles the file content in a streaming fashion to avoid loading large files into memory.
func writeJSONRecord(w io.Writer, path string, head []byte, rest io.Reader) error {
	pb, err := json.Marshal(path)
	if err != nil {
		return err
	}
	if _, err := w.Write([]byte(`{"path":`)); err != nil {
		return err
	}
	if _, err := w.Write(pb); err != nil {
		return err
	}
	if _, err := w.Write([]byte(`,"content":"`)); err != nil {
		return err
	}
	e := &jsonEscaper{dst: w}
	if _, err := e.Write(head); err != nil {
		return err
	}
	bufp := lineBufPool.Get().(*[]byte)
	if _, err := io.CopyBuffer(e, rest, *bufp); err != nil {
		lineBufPool.Put(bufp)
		return err
	}
	lineBufPool.Put(bufp)
	if err := e.Close(); err != nil {
		return err
	}
	_, err = w.Write([]byte("\"}"))
	return err
}
