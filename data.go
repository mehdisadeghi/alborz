package alborz

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"git.mehdix.org/alborz/record"
	bolt "go.etcd.io/bbolt"
)

// metaBucket says what the file holds and who has had it; see ADR 26.
var metaBucket = []byte("meta")

// formats are the buckets this version writes and the format of each.
// A format moves only when a bucket's records change beyond what the
// rules of ADR 26 absorb, and then under a new bucket name as well.
var formats = map[string]int{
	string(visitBucket): 1, string(readingBucket): 1, string(keptBucket): 1,
	// Named by the collections package, which does not export it.
	"collections": 1,
}

// openedKept is how many of the builds that opened the file are named,
// newest first: enough to see a rollback and what it rolled back from.
const openedKept = 8

// dataMeta is the meta bucket's one record.
type dataMeta struct {
	Formats map[string]int
	Opened  []dataOpened
}

type dataOpened struct {
	Build string
	At    time.Time
}

// openData opens alborz's data file in dir, making the file but never
// the directory, and notes this build as the latest to open it.
func openData(dir, build string, log func(format string, args ...any)) (*bolt.DB, error) {
	// The directory is whoever runs alborz to make, with the ownership
	// and the mode they meant. Alborz says which one is missing and
	// stops.
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return nil, fmt.Errorf("%s is not there: it holds remembered logins, reading settings and the "+
			"calendars and address books kept here; make it, or name another with -data-dir", dir)
	}
	path := filepath.Join(dir, storeFile)
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: openTimeout})
	if err != nil {
		// One process at a time holds the file. The usual reason for
		// waiting the whole timeout out is a second alborz on the same
		// data directory, and a message that does not say so sends the
		// reader looking at their disk.
		if errors.Is(err, bolt.ErrTimeout) {
			return nil, fmt.Errorf("%s is locked: another alborz is running on this data directory (waited %v); "+
				"stop it, or name another with -data-dir", path, openTimeout)
		}
		return nil, fmt.Errorf("failed to open %s: %w", path, err)
	}
	err = db.Update(func(tx *bolt.Tx) error {
		b, err := tx.CreateBucketIfNotExists(metaBucket)
		if err != nil {
			return err
		}
		old := b.Get(metaBucket)
		var meta dataMeta
		if old != nil {
			if err := json.Unmarshal(old, &meta); err != nil {
				return fmt.Errorf("%s: the meta record does not read: %w", path, err)
			}
		}
		if meta.Formats == nil {
			meta.Formats = map[string]int{}
		}
		for bucket, format := range formats {
			switch had := meta.Formats[bucket]; {
			case had > format:
				// Newer data is read and written under ADR 26's rules;
				// what this version does not know it hands back.
				log("%s: %s is in format %d, this build knows %d", path, bucket, had, format)
			case had < format:
				meta.Formats[bucket] = format
			}
		}
		meta.Opened = append([]dataOpened{{Build: build, At: time.Now().UTC()}}, meta.Opened...)
		if len(meta.Opened) > openedKept {
			meta.Opened = meta.Opened[:openedKept]
		}
		raw, err := record.Keep(old, &meta)
		if err != nil {
			return err
		}
		return b.Put(metaBucket, raw)
	})
	if err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}
