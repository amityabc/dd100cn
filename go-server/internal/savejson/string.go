package savejson

import (
	"bytes"
	"fmt"
	"strings"
	"unicode/utf16"
)

// The legacy store compares receipt Input as an exact string on retries.
// System.Text.Json's default encoder additionally escapes '+', apostrophes,
// backticks, and multibyte Unicode, including in the base64 report field.
type String string

func (s String) MarshalJSON() ([]byte, error) {
	var w bytes.Buffer
	w.WriteByte('"')
	for _, r := range s {
		switch r {
		case '\b':
			w.WriteString(`\b`)
		case '\t':
			w.WriteString(`\t`)
		case '\n':
			w.WriteString(`\n`)
		case '\f':
			w.WriteString(`\f`)
		case '\r':
			w.WriteString(`\r`)
		case '\\':
			w.WriteString(`\\`)
		default:
			if r >= 32 && r < 127 && !strings.ContainsRune("\"&'+<>`", r) {
				w.WriteRune(r)
				continue
			}
			if r <= 0xffff {
				fmt.Fprintf(&w, `\u%04X`, r)
			} else {
				a, b := utf16.EncodeRune(r)
				fmt.Fprintf(&w, `\u%04X\u%04X`, a, b)
			}
		}
	}
	w.WriteByte('"')
	return w.Bytes(), nil
}
