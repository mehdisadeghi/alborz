package mail

import (
	"bufio"
	"bytes"
	"strings"

	"github.com/emersion/go-message/textproto"
)

func readHeaderBytes(raw []byte) (textproto.Header, error) {
	return textproto.ReadHeader(bufio.NewReader(bytes.NewReader(raw)))
}

func normalizeMessageID(id string) string {
	return strings.Trim(strings.TrimSpace(id), "<>")
}
