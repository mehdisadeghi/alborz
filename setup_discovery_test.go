package alborz

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"sync/atomic"
	"testing"

	"golang.org/x/net/dns/dnsmessage"
)

func TestSetupDiscoveryIsOwnerGatedAndKeepsPartialResults(t *testing.T) {
	var queries atomic.Int64
	resolver := &net.Resolver{PreferGo: true, Dial: func(context.Context, string, string) (net.Conn, error) {
		queries.Add(1)
		client, server := net.Pipe()
		go func() {
			defer server.Close()
			var length [2]byte
			if _, err := io.ReadFull(server, length[:]); err != nil {
				return
			}
			body := make([]byte, binary.BigEndian.Uint16(length[:]))
			if _, err := io.ReadFull(server, body); err != nil {
				return
			}
			var query dnsmessage.Message
			if err := query.Unpack(body); err != nil {
				return
			}
			response := dnsmessage.Message{ID: query.ID, Response: true, Authoritative: true, Questions: query.Questions}
			for _, q := range query.Questions {
				if q.Name.String() == "_imaps._tcp.example.org." {
					response.Answers = append(response.Answers, dnsmessage.Resource{
						Header: dnsmessage.ResourceHeader{Name: q.Name, Type: dnsmessage.TypeSRV, Class: dnsmessage.ClassINET},
						Body:   &dnsmessage.SRVResource{Port: 993, Target: dnsmessage.MustNewName("mail.example.org.")},
					})
				}
			}
			body, err := response.Pack()
			if err != nil {
				return
			}
			binary.BigEndian.PutUint16(length[:], uint16(len(body)))
			server.Write(append(length[:], body...))
		}()
		return client, nil
	}}
	previous := net.DefaultResolver
	net.DefaultResolver = resolver
	t.Cleanup(func() { net.DefaultResolver = previous })
	s := newSetupServer(t, Options{})
	if _, err := s.DiscoverSetup(context.Background(), "wrong", "me@example.org"); !errors.Is(err, ErrSetupCode) {
		t.Fatal(err)
	}
	if queries.Load() != 0 {
		t.Fatal("unauthorized discovery queried DNS")
	}
	p, err := s.DiscoverSetup(context.Background(), s.SetupCode(), "me@example.org")
	var field *SetupError
	if !errors.As(err, &field) || field.Field != "discovery" {
		t.Fatalf("missing SMTP was not reported: %v", err)
	}
	if p.Domain != "example.org" || p.IMAP != "imaps://mail.example.org:993" || p.SMTP != "" {
		t.Fatalf("partial discovery lost usable IMAP: %+v", p)
	}
	if queries.Load() == 0 || !s.NeedsSetup() || len(s.Domains()) != 0 {
		t.Fatal("discovery changed configuration or did not use DNS")
	}
}
