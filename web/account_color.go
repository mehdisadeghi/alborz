package web

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
)

// AccountHue is independent of account order and membership. Similar hues are
// possible, so every marker also exposes the full address.
func AccountHue(account string) int {
	digest := sha256.Sum256([]byte(account))
	return int(binary.BigEndian.Uint16(digest[:2])) * 360 / 65536
}

// AccountPopoverID joins every row marker to one account disclosure per page.
func AccountPopoverID(account string) string {
	digest := sha256.Sum256([]byte(account))
	return fmt.Sprintf("account-%x", digest)
}
