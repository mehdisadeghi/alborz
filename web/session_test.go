package web

import (
	"mime/multipart"
	"testing"
	"time"
)

func TestFullAttachmentCacheStaysUsable(t *testing.T) {
	s, _ := newSession()
	big := &multipart.FileHeader{Size: MaxAttachmentSize}
	if _, err := s.putAttachment(big, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.putAttachment(big, nil); err != ErrAttachmentCacheSize {
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
