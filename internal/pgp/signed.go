// Package pgp reads the mechanics of signed mail: multipart/signed, Autocrypt and the Web Key Directory.
package pgp

import (
	"bufio"
	"bytes"
	"io"
)

// CRLF gives every bare LF its CR, the line ending MIME canonical form requires.
func CRLF(b []byte) []byte {
	out, _ := io.ReadAll(CRLFReader(bytes.NewReader(b)))
	return out
}

// CRLFReader is CRLF as the bytes stream by.
func CRLFReader(r io.Reader) io.Reader {
	return &crlfReader{r: bufio.NewReader(r)}
}

type crlfReader struct {
	r        *bufio.Reader
	prev     byte
	lfPended bool
}

func (c *crlfReader) Read(p []byte) (int, error) {
	n := 0
	for n < len(p) {
		if c.lfPended {
			p[n], c.prev, c.lfPended = '\n', '\n', false
			n++
			continue
		}
		b, err := c.r.ReadByte()
		if err != nil {
			return n, err
		}
		if b == '\n' && c.prev != '\r' {
			b, c.lfPended = '\r', true
		}
		p[n], c.prev = b, b
		n++
	}
	return n, nil
}
