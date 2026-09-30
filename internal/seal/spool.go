package seal

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"io"
	"os"
)

// spoolChunk is the plain length of every chunk but the last: large enough that the tag costs nothing,
// small enough that a reader holds one in memory.
const spoolChunk = 64 << 10

// Spool is a temporary file no name leads to, sealed under a key only memory holds, so what it keeps
// is unreadable on disk and goes with the process (ADR 65). It is the STREAM construction (Hoang,
// Reyhanitabar, Rogaway and Vizár, 2015) as age uses it: AES-256-GCM chunks under a nonce of the
// chunk's index and a flag marking the last, so a chunk moved, dropped or cut short does not open.
type Spool struct {
	f    *os.File
	aead cipher.AEAD
	size int64
}

// NewSpool spools what write writes; it is read back by Open, and Close lets it go.
func NewSpool(write func(io.Writer) error) (*Spool, error) {
	var key [KeySize]byte
	rand.Read(key[:])
	block, err := aes.NewCipher(key[:])
	if err != nil {
		panic(err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		panic(err)
	}
	f, err := os.CreateTemp("", "")
	if err != nil {
		return nil, err
	}
	// Unlinked, the file goes with its last descriptor, even if alborz dies (Unix).
	if err := os.Remove(f.Name()); err != nil {
		f.Close()
		return nil, err
	}
	s := &Spool{f: f, aead: aead}
	w := &spoolWriter{s: s, plain: make([]byte, 0, spoolChunk)}
	if err := write(w); err != nil {
		f.Close()
		return nil, err
	}
	if err := w.seal(true); err != nil {
		f.Close()
		return nil, err
	}
	return s, nil
}

type spoolWriter struct {
	s     *Spool
	plain []byte
	out   []byte
}

func (w *spoolWriter) Write(p []byte) (int, error) {
	n := len(p)
	for len(p) > 0 {
		if len(w.plain) == spoolChunk {
			if err := w.seal(false); err != nil {
				return n - len(p), err
			}
		}
		k := copy(w.plain[len(w.plain):spoolChunk], p)
		w.plain = w.plain[:len(w.plain)+k]
		p = p[k:]
	}
	return n, nil
}

// seal writes the chunk held; every chunk but the last is full, so the size so far counts them.
func (w *spoolWriter) seal(last bool) error {
	w.out = w.s.aead.Seal(w.out[:0], chunkNonce(w.s.size/spoolChunk, last), w.plain, nil)
	if _, err := w.s.f.Write(w.out); err != nil {
		return err
	}
	w.s.size += int64(len(w.plain))
	w.plain = w.plain[:0]
	return nil
}

// chunkNonce is the chunk's index in 11 bytes, then 1 for the last chunk and 0 for any other.
func chunkNonce(i int64, last bool) []byte {
	nonce := make([]byte, 12)
	binary.BigEndian.PutUint64(nonce[3:11], uint64(i))
	if last {
		nonce[11] = 1
	}
	return nonce
}

// Size is the plain length spooled.
func (s *Spool) Size() int64 { return s.size }

// Close lets the file go; readers opened fail after.
func (s *Spool) Close() error { return s.f.Close() }

// Open reads the spool from its start. Readers are independent and may read at once.
func (s *Spool) Open() io.ReadSeeker { return &spoolReader{s: s, chunk: -1} }

type spoolReader struct {
	s      *Spool
	off    int64
	chunk  int64
	plain  []byte
	sealed []byte
}

func (r *spoolReader) Read(p []byte) (int, error) {
	if r.off >= r.s.size {
		return 0, io.EOF
	}
	i := r.off / spoolChunk
	if i != r.chunk {
		if err := r.load(i); err != nil {
			return 0, err
		}
	}
	n := copy(p, r.plain[r.off-i*spoolChunk:])
	r.off += int64(n)
	return n, nil
}

func (r *spoolReader) load(i int64) error {
	last := i == (r.s.size-1)/spoolChunk
	n := int64(spoolChunk)
	if last {
		n = r.s.size - i*spoolChunk
	}
	overhead := int64(r.s.aead.Overhead())
	if r.sealed == nil {
		r.sealed = make([]byte, spoolChunk+overhead)
	}
	sealed := r.sealed[:n+overhead]
	if _, err := r.s.f.ReadAt(sealed, i*(spoolChunk+overhead)); err == io.EOF {
		return io.ErrUnexpectedEOF
	} else if err != nil {
		return err
	}
	plain, err := r.s.aead.Open(r.plain[:0], chunkNonce(i, last), sealed, nil)
	if err != nil {
		r.chunk = -1
		return ErrOpen
	}
	r.plain, r.chunk = plain, i
	return nil
}

func (r *spoolReader) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
	case io.SeekCurrent:
		offset += r.off
	case io.SeekEnd:
		offset += r.s.size
	default:
		return 0, errors.New("seal: invalid whence")
	}
	if offset < 0 {
		return 0, errors.New("seal: negative position")
	}
	r.off = offset
	return offset, nil
}
