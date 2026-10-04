package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"

	"git.mehdix.org/alborz/internal/kv"
	"git.mehdix.org/alborz/internal/record"
)

// ErrArchive is a collection in an archive that does not read.
var ErrArchive = errors.New("collections: the archive's collection does not read")

// collectionFile describes a collection in an archive; each object is a file of its own beside it, as a
// DAV client would have written it.
const collectionFile = "collection.json"

// archived holds the records as stored, fields a later version added among them (ADR 26); an object's
// record goes without its data, which is the file beside it.
type archived struct {
	Collection json.RawMessage
	Shares     []json.RawMessage `json:",omitempty"`
	Objects    []json.RawMessage `json:",omitempty"`
}

// Export puts owner's collections, each under its ID: its record, its shares and its objects.
func (s *Store) Export(owner string, put func(name string, data []byte) error) error {
	type file struct {
		name string
		data []byte
	}
	var files []file
	err := s.db.View(func(tx kv.Tx) error {
		return tx.Scan(s.collectionsOf(owner), func(_ string, raw []byte) error {
			var c Collection
			if err := json.Unmarshal(raw, &c); err != nil {
				return err
			}
			a := archived{Collection: clone(raw)}
			err := tx.Scan(s.sharesOf(c.Ref()), func(_ string, raw []byte) error {
				a.Shares = append(a.Shares, clone(raw))
				return nil
			})
			if err != nil {
				return err
			}
			err = tx.Scan(s.objectsOf(c.Ref()), func(_ string, raw []byte) error {
				var fields map[string]json.RawMessage
				var o Object
				if err := json.Unmarshal(raw, &fields); err != nil {
					return err
				}
				if err := json.Unmarshal(raw, &o); err != nil {
					return err
				}
				delete(fields, "Data")
				meta, err := json.Marshal(fields)
				if err != nil {
					return err
				}
				a.Objects = append(a.Objects, meta)
				files = append(files, file{c.ID + "/" + o.Name, o.Data})
				return nil
			})
			if err != nil {
				return err
			}
			described, err := json.MarshalIndent(a, "", "  ")
			files = append(files, file{c.ID + "/" + collectionFile, described})
			return err
		})
	})
	if err != nil {
		return err
	}
	for _, f := range files {
		if err := put(f.name, f.data); err != nil {
			return err
		}
	}
	return nil
}

// The values a scan hands over live only as long as its transaction.
func clone(raw []byte) json.RawMessage { return append(json.RawMessage(nil), raw...) }

// Import restores the collections Export put, as owner's whatever owned them before, and names those whose
// publication it did not keep: their secret serves another collection here, which keeps it.
func (s *Store) Import(owner string, files map[string][]byte) (unpublished []string, err error) {
	owner = strings.ToLower(owner)
	for name, raw := range files {
		id, ok := strings.CutSuffix(name, "/"+collectionFile)
		if !ok {
			continue
		}
		var a archived
		var c Collection
		if json.Unmarshal(raw, &a) != nil || json.Unmarshal(a.Collection, &c) != nil || c.ID != id || !plain(id) {
			return unpublished, fmt.Errorf("%w: %s", ErrArchive, name)
		}
		c.Owner = owner
		kept, err := s.restore(c, a, files)
		if err != nil {
			return unpublished, err
		}
		if !kept {
			unpublished = append(unpublished, c.Name)
		}
	}
	return unpublished, nil
}

// restore writes a collection with its shares and objects over any of the same names, each record the
// archive's with only what must change rewritten; what it does not name stays. The ctag moves, so a client
// looks again. It reports false where the publication was dropped.
func (s *Store) restore(c Collection, a archived, files map[string][]byte) (published bool, err error) {
	ref := c.Ref()
	published = true
	err = s.change(ref, func(tx kv.Tx) error {
		if owner := s.ring.Name(c.Owner); s.ring.On(owner) {
			if err := keyed(tx).encrypt(s.ref(ref), []string{owner}); err != nil {
				return err
			}
		}
		before := ""
		if held, err := get[Collection](tx, s.collectionKey(ref)); err == nil {
			before = held.Public
		} else if !errors.Is(err, ErrNotFound) {
			return err
		}
		if c.Public != "" {
			if at := tx.Get(s.publicKey(c.Public)); at != nil && string(at) != s.collectionKey(ref) {
				c.Public, published = "", false
			}
		}
		if err := stamp(tx, &c); err != nil {
			return err
		}
		if err := over(tx, s.collectionKey(ref), a.Collection, &c); err != nil {
			return err
		}
		if err := s.publish(tx, before, c); err != nil {
			return err
		}
		for _, raw := range a.Objects {
			var o Object
			if err := json.Unmarshal(raw, &o); err != nil || !plain(o.Name) {
				return fmt.Errorf("%w: %s/%s", ErrArchive, c.ID, o.Name)
			}
			data, ok := files[c.ID+"/"+o.Name]
			if !ok {
				return fmt.Errorf("%w: %s/%s", ErrArchive, c.ID, o.Name)
			}
			o.Data, o.ETag = data, ETagOf(data)
			if err := over(tx, s.objectKey(ref, o.Name), raw, &o); err != nil {
				return err
			}
			if o.UID == "" {
				continue
			}
			if err := tx.Put(s.uidKey(ref, o.UID), []byte(o.Name)); err != nil {
				return err
			}
		}
		for _, raw := range a.Shares {
			var sh Share
			if err := json.Unmarshal(raw, &sh); err != nil {
				return fmt.Errorf("%w: %s", ErrArchive, c.ID)
			}
			sh.Owner, sh.Collection, sh.To = c.Owner, c.ID, strings.ToLower(sh.To)
			if sh.To == c.Owner {
				continue
			}
			if err := over(tx, s.shareKey(ref, sh.To), raw, &sh); err != nil {
				return err
			}
			if err := put(tx, s.receivedKey(sh.To, ref), ref); err != nil {
				return err
			}
			if err := keyed(tx).wrapTo(s.ref(ref), s.ring.Name(sh.To)); err != nil {
				return err
			}
		}
		return nil
	})
	return published, err
}

// over writes v with the fields of raw it does not know, replacing what was stored (ADR 27).
func over(tx kv.Tx, key string, raw []byte, v any) error {
	merged, err := record.Keep(raw, v)
	if err != nil {
		return err
	}
	return tx.Put(key, merged)
}

// plain is a name that is one path segment, as a collection's or an object's is.
func plain(name string) bool {
	return name != "" && name != "." && name != ".." && path.Base(name) == name
}
