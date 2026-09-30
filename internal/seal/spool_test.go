package seal

import (
	"bytes"
	"crypto/rand"
	"errors"
	"io"
	"testing"
)

func spooled(t *testing.T, plain []byte) *Spool {
	t.Helper()
	s, err := NewSpool(func(w io.Writer) error {
		_, err := io.Copy(w, bytes.NewReader(plain))
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func onDisk(t *testing.T, s *Spool) []byte {
	t.Helper()
	fi, err := s.f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	raw := make([]byte, fi.Size())
	if _, err := s.f.ReadAt(raw, 0); err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestSpoolReadsBackWhatWasWrittenFromAnyPosition(t *testing.T) {
	for _, size := range []int{0, 1, spoolChunk, 3*spoolChunk + 123} {
		plain := []byte(rand.Text())
		plain = bytes.Repeat(plain, size/len(plain)+1)[:size]
		s := spooled(t, plain)
		if s.Size() != int64(size) {
			t.Fatalf("size %d spooled as %d", size, s.Size())
		}
		// Shorter than 16 bytes, a probe turns up in random ciphertext by chance.
		if size >= 16 && bytes.Contains(onDisk(t, s), plain[:16]) {
			t.Fatalf("size %d: the file holds the plain text", size)
		}
		r := s.Open()
		for _, at := range []int64{int64(size) / 2, 0, int64(size) - 1, spoolChunk - 1} {
			if at < 0 || at >= int64(size) {
				continue
			}
			if _, err := r.Seek(at, io.SeekStart); err != nil {
				t.Fatal(err)
			}
			got, err := io.ReadAll(r)
			if err != nil || !bytes.Equal(got, plain[at:]) {
				t.Fatalf("size %d, from %d: %d bytes back, %v", size, at, len(got), err)
			}
		}
		if end, err := r.Seek(0, io.SeekEnd); err != nil || end != int64(size) {
			t.Fatalf("size %d: end at %d, %v", size, end, err)
		}
	}
}

func TestSpoolRefusesAChunkCutOrMoved(t *testing.T) {
	plain := bytes.Repeat([]byte("x"), 3*spoolChunk)
	chunk := int64(spoolChunk + 16)

	cut := spooled(t, plain)
	if err := cut.f.Truncate(2 * chunk); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadAll(cut.Open()); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("a spool cut short read as %v", err)
	}

	swapped := spooled(t, plain)
	raw := onDisk(t, swapped)
	first := bytes.Clone(raw[:chunk])
	copy(raw, raw[chunk:2*chunk])
	copy(raw[chunk:], first)
	if _, err := swapped.f.WriteAt(raw, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadAll(swapped.Open()); !errors.Is(err, ErrOpen) {
		t.Fatalf("a spool with two chunks swapped read as %v", err)
	}
}
