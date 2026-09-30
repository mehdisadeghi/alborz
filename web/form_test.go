package web

import (
	"bytes"
	"crypto/rand"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// onDisk is what the process keeps in files: those in the temp dir, and those open but unlinked.
func onDisk(t *testing.T) [][]byte {
	t.Helper()
	var files [][]byte
	temp, err := os.ReadDir(os.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range temp {
		if b, err := os.ReadFile(filepath.Join(os.TempDir(), e.Name())); err == nil {
			files = append(files, b)
		}
	}
	fds, err := os.ReadDir("/dev/fd")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range fds {
		// On macOS this shares the descriptor's offset, so it is read only by ReadAt.
		f, err := os.Open(filepath.Join("/dev/fd", e.Name()))
		if err != nil {
			continue
		}
		fi, err := f.Stat()
		if err == nil && fi.Mode().IsRegular() && fi.Sys().(*syscall.Stat_t).Nlink == 0 {
			b := make([]byte, fi.Size())
			if _, err := f.ReadAt(b, 0); err != nil {
				t.Fatal(err)
			}
			files = append(files, b)
		}
		f.Close()
	}
	return files
}

func TestAnUploadIsNeverOnDiskUnsealed(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	marker := []byte(rand.Text())
	const size = 33 << 20

	body, feed := io.Pipe()
	mw := multipart.NewWriter(feed)
	req := httptest.NewRequest(http.MethodPost, "/", body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	ctx := &Context{request: req, response: &Response{ResponseWriter: httptest.NewRecorder()}}
	defer func() { ctx.form.Close() }()
	var got []byte
	h := func(ctx *Context) error {
		f, err := ctx.FormFile("file")
		if err != nil {
			return err
		}
		got, err = io.ReadAll(f.Open())
		return err
	}
	done := make(chan error, 1)
	go func() { done <- (&Group{}).bounded(http.MethodPost, 2*size, h, refuseTooLarge)(ctx) }()

	part, err := mw.CreateFormFile("file", "big.bin")
	if err != nil {
		t.Fatal(err)
	}
	sent := bytes.Repeat(marker, size/len(marker)+1)
	if _, err := part.Write(sent); err != nil {
		t.Fatal(err)
	}
	// The body is still open: what the server took so far is on disk.
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		files := onDisk(t)
		var n int
		for _, b := range files {
			n += len(b)
			if bytes.Contains(b, marker) {
				t.Fatal("a file on disk holds the upload's plain text")
			}
		}
		if n >= size-1<<20 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d bytes on disk; the upload was not spooled", n)
		}
	}
	mw.Close()
	feed.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, sent) {
		t.Fatalf("the handler read %d bytes of the %d sent", len(got), len(sent))
	}
}
