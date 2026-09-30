package mail

import (
	"fmt"
	"net/mail"
	"net/url"
	"strconv"
	"strings"

	"github.com/emersion/go-imap/v2"
)

// ParseUID reads a message's UID, which is never zero.
func ParseUID(s string) (imap.UID, error) {
	uid, err := strconv.ParseUint(s, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("invalid UID: %v", err)
	}
	if uid == 0 {
		return 0, fmt.Errorf("UID must be non-zero")
	}
	return imap.UID(uid), nil
}

// ParseMessageRef reads the mailbox and uid a form names.
func ParseMessageRef(mboxString, uidString string) (string, imap.UID, error) {
	return parseMboxAndUid(mboxString, uidString)
}

func parseMboxAndUid(mboxString, uidString string) (string, imap.UID, error) {
	mboxName, err := url.PathUnescape(mboxString)
	if err != nil {
		return "", 0, fmt.Errorf("invalid mailbox name: %v", err)
	}
	uid, err := ParseUID(uidString)
	return mboxName, uid, err
}

func parseUidList(values []string) ([]imap.UID, error) {
	var uids []imap.UID
	for _, v := range values {
		uid, err := ParseUID(v)
		if err != nil {
			return nil, err
		}
		uids = append(uids, uid)
	}
	return uids, nil
}

// ParsePartPath reads a part's path, as "1.2"; empty is the whole message.
func ParsePartPath(s string) ([]int, error) {
	if s == "" {
		return nil, nil
	}

	l := strings.Split(s, ".")
	path := make([]int, len(l))
	for i, s := range l {
		var err error
		path[i], err = strconv.Atoi(s)
		if err != nil {
			return nil, err
		}

		if path[i] <= 0 {
			return nil, fmt.Errorf("part num must be strictly positive")
		}
	}
	return path, nil
}

// Uses net/mail, since a display name may contain a comma.
func parseAddressList(s string) ([]string, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	list, err := mail.ParseAddressList(s)
	if err != nil {
		return nil, err
	}
	addresses := make([]string, len(list))
	for i, addr := range list {
		addresses[i] = addr.String()
	}
	return addresses, nil
}
