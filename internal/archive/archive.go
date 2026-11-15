// Package archive is the owner's local, append-only copy of every event the
// daemon emits (~/.liveshortly/archive.db). Replay/read-back is a later phase.
package archive

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "modernc.org/sqlite" // pure-go, keeps CGO_ENABLED=0 builds working
)

type DB struct{ db *sql.DB }

// Open creates/opens the archive at path (parent dir created if missing).
func Open(path string) (*DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS events(
		session_id TEXT NOT NULL, seq INTEGER NOT NULL DEFAULT 0, ord INTEGER NOT NULL,
		actor TEXT, event_type TEXT, payload BLOB, ts TEXT,
		UNIQUE(session_id, ord));
		CREATE INDEX IF NOT EXISTS events_session ON events(session_id)`)
	if err != nil {
		db.Close()
		return nil, err
	}
	return &DB{db}, nil
}

// Append stores one event. ord==0 allocates the next per-session ordinal;
// a caller-supplied ord makes a repeat of the same (session, ord) a no-op.
// seq is the server seq when known, else 0.
func (d *DB) Append(session string, seq, ord int, actor, eventType string, payload map[string]any, ts time.Time) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if ord == 0 {
		_, err = d.db.Exec(`INSERT OR IGNORE INTO events
			SELECT ?1, ?2, COALESCE(MAX(ord),0)+1, ?3, ?4, ?5, ?6 FROM events WHERE session_id=?1`,
			session, seq, actor, eventType, b, ts.UTC().Format(time.RFC3339Nano))
		return err
	}
	_, err = d.db.Exec(`INSERT OR IGNORE INTO events VALUES(?,?,?,?,?,?,?)`,
		session, seq, ord, actor, eventType, b, ts.UTC().Format(time.RFC3339Nano))
	return err
}

var (
	defOnce sync.Once
	defDB   *DB
	defErr  error
)

// Default lazily opens the shared ~/.liveshortly/archive.db once per process.
func Default() (*DB, error) {
	defOnce.Do(func() {
		home, err := os.UserHomeDir()
		if err != nil {
			defErr = err
			return
		}
		defDB, defErr = Open(filepath.Join(home, ".liveshortly", "archive.db"))
	})
	return defDB, defErr
}
