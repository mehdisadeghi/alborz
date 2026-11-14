package alborztest

import (
	"context"
	"encoding/binary"
	"io"
	"net"

	"golang.org/x/net/dns/dnsmessage"
)

// Tests never ask the network's DNS: every name is unknown unless a test
// installs a Resolver of its own. /etc/hosts still answers localhost.
func init() {
	net.DefaultResolver = Resolver(nil)
}

// Resolver answers DNS queries in memory over a pipe. answer returns the
// records for a question; none is a non-existent domain (NXDOMAIN).
func Resolver(answer func(dnsmessage.Question) []dnsmessage.Resource) *net.Resolver {
	return &net.Resolver{PreferGo: true, Dial: func(context.Context, string, string) (net.Conn, error) {
		client, server := net.Pipe()
		go serveDNS(server, answer)
		return client, nil
	}}
}

// serveDNS answers one query in the TCP framing the Go resolver uses on a stream.
func serveDNS(conn net.Conn, answer func(dnsmessage.Question) []dnsmessage.Resource) {
	defer conn.Close()
	var length [2]byte
	if _, err := io.ReadFull(conn, length[:]); err != nil {
		return
	}
	body := make([]byte, binary.BigEndian.Uint16(length[:]))
	if _, err := io.ReadFull(conn, body); err != nil {
		return
	}
	var query dnsmessage.Message
	if err := query.Unpack(body); err != nil {
		return
	}
	response := dnsmessage.Message{
		Header:    dnsmessage.Header{ID: query.ID, Response: true, Authoritative: true},
		Questions: query.Questions,
	}
	for _, q := range query.Questions {
		if answer != nil {
			response.Answers = append(response.Answers, answer(q)...)
		}
	}
	if len(response.Answers) == 0 {
		response.RCode = dnsmessage.RCodeNameError
	}
	out, err := response.Pack()
	if err != nil {
		return
	}
	binary.BigEndian.PutUint16(length[:], uint16(len(out)))
	conn.Write(append(length[:], out...))
}
