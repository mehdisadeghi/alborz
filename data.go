package alborz

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"git.mehdix.org/alborz/internal/kv"
	"git.mehdix.org/alborz/internal/kv/bolt"
	"git.mehdix.org/alborz/internal/record"
	"git.mehdix.org/alborz/plugins/collections"
)

// metaKey says what the file holds and who has had it; see ADR 26.
const metaKey = "alborz.meta"

// formats are the key owners this version writes, with the format of
// each (ADR 26).
var formats = map[string]int{
	sessionsOwner: recordsFormat, settingsOwner: recordsFormat,
	collections.Name: collections.Format,
}

// openedKept is how many of the builds that opened the file are named,
// newest first: enough to see a rollback and what it rolled back from.
const openedKept = 8

// dataMeta is the meta record.
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
func openData(dir, build string, log func(format string, args ...any)) (kv.DB, error) {
	// The directory is the operator's to make, with the owner and mode
	// they mean.
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return nil, fmt.Errorf("%s is not there: it holds remembered logins, settings and the "+
			"calendars and address books kept here; make it, or name another with -data-dir", dir)
	}
	path := filepath.Join(dir, storeFile)
	db, err := bolt.Open(path, openTimeout)
	switch {
	// A lock held that long is almost always a second alborz.
	case errors.Is(err, kv.ErrLocked):
		return nil, fmt.Errorf("%s is locked: another alborz is running on this data directory (waited %v); "+
			"stop it, or name another with -data-dir", path, openTimeout)
	case err != nil:
		return nil, fmt.Errorf("failed to open %s: %w", path, err)
	}
	err = db.Update(func(tx kv.Tx) error {
		old := tx.Get(metaKey)
		var meta dataMeta
		if old != nil {
			if err := json.Unmarshal(old, &meta); err != nil {
				return fmt.Errorf("%s: the meta record does not read: %w", path, err)
			}
		}
		if meta.Formats == nil {
			meta.Formats = map[string]int{}
		}
		for owner, format := range formats {
			switch had := meta.Formats[owner]; {
			case had > format:
				// Still read and written, under ADR 26's rules.
				log("%s: %s is in format %d, this build knows %d", path, owner, had, format)
			case had < format:
				meta.Formats[owner] = format
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
		return tx.Put(metaKey, raw)
	})
	if err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}
