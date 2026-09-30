package web

import (
	"io"
	"testing"
	"time"

	"git.mehdix.org/alborz/internal/seal"
)

func TestFullAttachmentCacheStaysUsable(t *testing.T) {
	s, _ := newSession()
	body, err := seal.NewSpool(func(w io.Writer) error {
		_, err := w.Write(make([]byte, MaxAttachmentSize))
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	big := &File{Spool: body}
	if _, err := s.putAttachment(big); err != nil {
		t.Fatal(err)
	}
	if _, err := s.putAttachment(big); err != ErrAttachmentCacheSize {
		t.Fatalf("a second full-size attachment was taken: %v", err)
	}
	done := make(chan struct{})
	go func() {
		s.popAttachment("none")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("the cache stayed locked after refusing an attachment")
	}
}
