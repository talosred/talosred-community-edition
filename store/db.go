package store

import (
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
)

func Open(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path+"?_journal_mode=WAL&_foreign_keys=on")
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1) // sqlite WAL allows one writer
	return db, nil
}

func Migrate(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS requests (
			id          TEXT PRIMARY KEY,
			ts          INTEGER NOT NULL,
			provider    TEXT NOT NULL,
			model       TEXT NOT NULL,
			input_tok   INTEGER,
			output_tok  INTEGER,
			ttft_ms     INTEGER,
			latency_ms  INTEGER,
			cost_usd    REAL,
			req_json    TEXT,
			res_json    TEXT
		);
		CREATE INDEX IF NOT EXISTS idx_requests_ts       ON requests(ts DESC);
		CREATE INDEX IF NOT EXISTS idx_requests_provider ON requests(provider);
		CREATE INDEX IF NOT EXISTS idx_requests_model    ON requests(model);
	`)
	return err
}
