package alborzbase

import (
	"fmt"
	"net/mail"
	"net/url"
	"strconv"
	"strings"

	"github.com/emersion/go-imap/v2"
)

func parseUid(s string) (imap.UID, error) {
	uid, err := strconv.ParseUint(s, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("invalid UID: %v", err)
	}
	if uid == 0 {
		return 0, fmt.Errorf("UID must be non-zero")
	}
	return imap.UID(uid), nil
}

// ParseMessageRef reads the mailbox and uid a form names, for a plugin
// that acts on a message it did not render.
func ParseMessageRef(mboxString, uidString string) (string, imap.UID, error) {
	return parseMboxAndUid(mboxString, uidString)
}

func parseMboxAndUid(mboxString, uidString string) (string, imap.UID, error) {
	mboxName, err := url.PathUnescape(mboxString)
	if err != nil {
		return "", 0, fmt.Errorf("invalid mailbox name: %v", err)
	}
	uid, err := parseUid(uidString)
	return mboxName, uid, err
}

func parseUidList(values []string) ([]imap.UID, error) {
	var uids []imap.UID
	for _, v := range values {
		uid, err := parseUid(v)
		if err != nil {
			return nil, err
		}
		uids = append(uids, uid)
	}
	return uids, nil
}

func parsePartPath(s string) ([]int, error) {
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

// parseAddressList reads one header field's worth of recipients.
// Splitting on commas cannot do this: a display name is allowed to
// contain one, and "Doe, John" <j@x.com> then becomes two broken
// recipients. net/mail knows the grammar.
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
