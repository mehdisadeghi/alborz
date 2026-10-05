package alborz

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"path"
	"slices"
	"strings"
	"time"
)

// An account's archive is a gzipped tar of plain files: what alborz keeps for it, opened, for the reader to
// keep and to import into this or another alborz (ADR 27).
const (
	archiveManifest = AppName + ".json"
	archiveFormat   = 1
	// ArchiveMaxSize bounds an archive read: it is held whole, so a broken one writes nothing.
	ArchiveMaxSize = 256 << 20
	settingsDir    = "settings"
)

// ErrNotArchive is a file that is not an archive alborz wrote, or one cut short.
var ErrNotArchive = errors.New("alborz: not an alborz archive")

type archiveManifestRecord struct {
	Format   int
	Account  string
	Exported time.Time
	Build    string `json:",omitempty"`
}

// An ArchivePart is a feature's share of the archive, its files under Dir.
type ArchivePart struct {
	Dir    string
	Export func(ctx context.Context, l *Login, put func(name string, data []byte) error) error
	// Import takes the part's files by their names under Dir, and writes them for the login's account.
	Import func(ctx context.Context, l *Login, files map[string][]byte) ([]ImportNote, error)
}

// An ImportNote is something an import did not carry over as the archive had it: Key is the sentence
// that says so, Name what it was.
type ImportNote struct{ Key, Name string }

// Export writes the login's archive to w.
func (s *Server) Export(ctx context.Context, l *Login, w io.Writer) error {
	z := gzip.NewWriter(w)
	tw := tar.NewWriter(z)
	now := time.Now().UTC()
	put := func(name string, data []byte) error {
		err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(data)), ModTime: now, Typeflag: tar.TypeReg})
		if err != nil {
			return err
		}
		_, err = tw.Write(data)
		return err
	}
	build, _ := Build()
	manifest, err := json.MarshalIndent(archiveManifestRecord{Format: archiveFormat, Account: l.Username(), Exported: now, Build: build}, "", "  ")
	if err != nil {
		return err
	}
	if err := put(archiveManifest, manifest); err != nil {
		return err
	}
	for _, part := range s.archiveParts() {
		err := part.Export(ctx, l, func(name string, data []byte) error { return put(part.Dir+"/"+name, data) })
		if err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return z.Close()
}

// Import writes what the archive in r holds for the login's account; what it does not name stays.
func (s *Server) Import(ctx context.Context, l *Login, r io.Reader) ([]ImportNote, error) {
	files, err := readArchive(r)
	if err != nil {
		return nil, err
	}
	var notes []ImportNote
	for _, part := range s.archiveParts() {
		mine := map[string][]byte{}
		for name, data := range files {
			if rest, ok := strings.CutPrefix(name, part.Dir+"/"); ok {
				mine[rest] = data
			}
		}
		n, err := part.Import(ctx, l, mine)
		notes = append(notes, n...)
		if err != nil {
			return notes, err
		}
	}
	return notes, nil
}

func (s *Server) archiveParts() []ArchivePart {
	return append([]ArchivePart{{Dir: settingsDir, Export: exportSettings, Import: importSettings}}, s.Archive...)
}

// readArchive reads every file, up to ArchiveMaxSize in all; a file of a part this build does not know is
// read and left alone.
func readArchive(r io.Reader) (map[string][]byte, error) {
	z, err := gzip.NewReader(r)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNotArchive, err)
	}
	limited := &io.LimitedReader{R: z, N: ArchiveMaxSize + 1}
	tr := tar.NewReader(limited)
	files := map[string][]byte{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrNotArchive, err)
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		name := path.Clean(h.Name)
		if path.IsAbs(name) || name == ".." || strings.HasPrefix(name, "../") {
			return nil, fmt.Errorf("%w: %q leaves the archive", ErrNotArchive, h.Name)
		}
		if files[name], err = io.ReadAll(tr); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrNotArchive, err)
		}
	}
	if limited.N <= 0 {
		return nil, fmt.Errorf("%w: over %d MiB", ErrNotArchive, ArchiveMaxSize>>20)
	}
	var manifest archiveManifestRecord
	if err := json.Unmarshal(files[archiveManifest], &manifest); err != nil || manifest.Format == 0 {
		return nil, fmt.Errorf("%w: no %s", ErrNotArchive, archiveManifest)
	}
	return files, nil
}

// The HTTP password is sealed under the mail password, which does not travel (ADR 5), and the account's own
// key opens nothing the archive holds (ADR 72).
func exportSettings(ctx context.Context, l *Login, put func(string, []byte) error) error {
	settings, ok := l.Store().(Settings)
	if !ok {
		return nil
	}
	names, err := settings.Entries(ctx)
	if err != nil {
		return err
	}
	for _, name := range names {
		name = strings.TrimPrefix(name, vendorRoot+"/")
		if name == httpPasswordKey || name == keyEntry {
			continue
		}
		var raw json.RawMessage
		if err := settings.Get(ctx, name, &raw); err != nil {
			return err
		}
		// An entry that does not read is left out; the store has said so.
		if raw == nil {
			continue
		}
		if err := put(name+".json", raw); err != nil {
			return err
		}
	}
	return nil
}

// One entry that does not read, or that the mail server will not keep, leaves the others to be imported.
func importSettings(ctx context.Context, l *Login, files map[string][]byte) ([]ImportNote, error) {
	var notes []ImportNote
	for _, file := range slices.Sorted(maps.Keys(files)) {
		name, ok := strings.CutSuffix(file, ".json")
		if !ok || name == httpPasswordKey || name == keyEntry {
			continue
		}
		if !json.Valid(files[file]) {
			notes = append(notes, ImportNote{Key: "data.entryunread", Name: name})
			continue
		}
		err := l.Store().Put(ctx, name, json.RawMessage(files[file]))
		if errors.As(err, new(StoreRefusedError)) {
			l.manager.logger.Printf("%s: import: the store refused %s: %v", l.username, name, err)
			notes = append(notes, ImportNote{Key: "data.entryrefused", Name: name})
			continue
		} else if err != nil {
			return notes, err
		}
	}
	return notes, nil
}
