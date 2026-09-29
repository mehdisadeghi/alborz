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
)

// metaKey says what the file holds and who has had it; see ADR 26.
const metaKey = "alborz.meta"

// formats are core's key owners, with each one's format; a feature notes its own through Server.Data.
var formats = map[string]int{sessionsOwner: recordsFormat, settingsOwner: recordsFormat}

// maxOpened is enough builds to see a rollback and what it rolled back from.
const maxOpened = 8

type dataMeta struct {
	Formats map[string]int
	Opened  []dataOpened
}

type dataOpened struct {
	Build string
	At    time.Time
}

// openData opens alborz's data file in dir and notes this build as the latest to open it.
func openData(dir, build string, log func(format string, args ...any)) (kv.DB, error) {
	// The directory is the operator's to make, with the owner and mode they mean.
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return nil, fmt.Errorf("%s is not there: it holds remembered logins, settings and the "+
			"calendars and address books kept here; make it, or name another with -data-dir", dir)
	}
	path := filepath.Join(dir, storeFile)
	db, err := bolt.Open(path, openTimeout)
	switch {
	case errors.Is(err, kv.ErrLocked):
		return nil, fmt.Errorf("%s is locked: another alborz is running on this data directory (waited %v); "+
			"stop it, or name another with -data-dir", path, openTimeout)
	case err != nil:
		return nil, fmt.Errorf("failed to open %s: %w", path, err)
	}
	err = updateMeta(db, path, func(meta *dataMeta) {
		noteFormats(meta, formats, path, log)
		meta.Opened = append([]dataOpened{{Build: build, At: time.Now().UTC()}}, meta.Opened...)
		if len(meta.Opened) > maxOpened {
			meta.Opened = meta.Opened[:maxOpened]
		}
	})
	if err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// Data is alborz's data file for a feature that keeps keys under owner, in format.
func (s *Server) Data(owner string, format int) (kv.DB, error) {
	err := updateMeta(s.data, s.dataPath, func(meta *dataMeta) {
		noteFormats(meta, map[string]int{owner: format}, s.dataPath, s.e.Logger.Printf)
	})
	return s.data, err
}

func updateMeta(db kv.DB, path string, change func(*dataMeta)) error {
	return db.Update(func(tx kv.Tx) error {
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
		change(&meta)
		raw, err := record.Keep(old, &meta)
		if err != nil {
			return err
		}
		return tx.Put(metaKey, raw)
	})
}

func noteFormats(meta *dataMeta, formats map[string]int, path string, log func(format string, args ...any)) {
	for owner, format := range formats {
		switch had := meta.Formats[owner]; {
		case had > format:
			log("%s: %s is in format %d, this build knows %d", path, owner, had, format)
		case had < format:
			meta.Formats[owner] = format
		}
	}
}
