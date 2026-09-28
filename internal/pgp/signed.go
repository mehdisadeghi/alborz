// Package pgp reads the mechanics of signed mail: multipart/signed, Autocrypt and the Web Key Directory.
package pgp

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/emersion/go-message"
	"github.com/emersion/go-message/textproto"
)

// Signed splits a message whose root is multipart/signed (RFC 1847) into
// the first part exactly as transmitted and the second part's decoded body.
// Only the root counts: a signed part nested in unsigned mail says nothing
// about the message around it.
func Signed(raw []byte) (signed, sig []byte, protocol string, err error) {
	raw = CRLF(raw)
	h, err := textproto.ReadHeader(bufio.NewReader(bytes.NewReader(raw)))
	if err != nil {
		return nil, nil, "", err
	}
	root := message.Header{Header: h}
	mediaType, params, err := root.ContentType()
	if err != nil {
		return nil, nil, "", err
	}
	if mediaType != "multipart/signed" {
		return nil, nil, "", fmt.Errorf("root is %s, not multipart/signed", mediaType)
	}
	protocol = strings.ToLower(params["protocol"])
	boundary := params["boundary"]
	if protocol == "" || boundary == "" {
		return nil, nil, "", errors.New("multipart/signed without protocol or boundary")
	}
	end := bytes.Index(raw, []byte("\r\n\r\n"))
	if end < 0 {
		return nil, nil, "", errors.New("message has no body")
	}
	// Keeping the CRLF that ends the header lets the first delimiter be found like any other.
	body := raw[end+2:]
	delim := []byte("\r\n--" + boundary)

	_, first, _, err := delimiter(body, 0, delim)
	if err != nil {
		return nil, nil, "", err
	}
	signedEnd, second, _, err := delimiter(body, first-2, delim)
	if err != nil {
		return nil, nil, "", err
	}
	sigEnd, _, last, err := delimiter(body, second-2, delim)
	if err != nil {
		return nil, nil, "", err
	}
	if !last {
		return nil, nil, "", errors.New("multipart/signed has more than two parts")
	}

	part, err := message.Read(bytes.NewReader(body[second:sigEnd]))
	if err != nil {
		return nil, nil, "", err
	}
	if sigType, _, err := part.Header.ContentType(); err != nil || sigType != protocol {
		return nil, nil, "", fmt.Errorf("signature part is %s, protocol is %s", sigType, protocol)
	}
	sig, err = io.ReadAll(part.Body)
	if err != nil {
		return nil, nil, "", err
	}
	return body[first:signedEnd], sig, protocol, nil
}

// delimiter finds the delimiter line at or after from (RFC 2046 5.1.1):
// start is where its leading CRLF begins, which belongs to the delimiter,
// and next is where the following part begins.
func delimiter(body []byte, from int, delim []byte) (start, next int, last bool, err error) {
	for {
		i := bytes.Index(body[from:], delim)
		if i < 0 {
			return 0, 0, false, errors.New("multipart/signed delimiter missing")
		}
		start = from + i
		rest := body[start+len(delim):]
		last = bytes.HasPrefix(rest, []byte("--"))
		if last {
			rest = rest[2:]
		}
		eol := bytes.Index(rest, []byte("\r\n"))
		if eol < 0 {
			eol = len(rest)
		}
		if len(bytes.Trim(rest[:eol], " \t")) == 0 {
			next = len(body) - len(rest) + min(eol+2, len(rest))
			return start, next, last, nil
		}
		from = start + 1
	}
}

// CRLF gives every bare LF its CR, the line ending MIME canonical form requires.
func CRLF(b []byte) []byte {
	var out bytes.Buffer
	out.Grow(len(b) + len(b)/16)
	for i, c := range b {
		if c == '\n' && (i == 0 || b[i-1] != '\r') {
			out.WriteByte('\r')
		}
		out.WriteByte(c)
	}
	return out.Bytes()
}
