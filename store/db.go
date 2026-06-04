package store

import (
	"database/sql"
	"fmt"
	"strings"

	_ "modernc.org/sqlite"
)

func Open(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path+"?_journal_mode=WAL&_foreign_keys=on")
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

func Migrate(db *sql.DB) error {
	if _, err := db.Exec(`
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

		CREATE TABLE IF NOT EXISTS model_pricing (
			model          TEXT PRIMARY KEY,
			provider       TEXT NOT NULL DEFAULT '',
			input_per_1k   REAL NOT NULL DEFAULT 0,
			output_per_1k  REAL NOT NULL DEFAULT 0,
			updated_at     INTEGER NOT NULL DEFAULT 0
		);

		CREATE TABLE IF NOT EXISTS model_aliases (
			pattern        TEXT PRIMARY KEY,
			target_model   TEXT NOT NULL,
			target_url     TEXT NOT NULL DEFAULT '',
			provider       TEXT NOT NULL DEFAULT 'openai',
			enabled        INTEGER NOT NULL DEFAULT 1,
			updated_at     INTEGER NOT NULL DEFAULT 0
		);
	`); err != nil {
		return fmt.Errorf("migrate schema: %w", err)
	}

	// Additive columns on requests — idempotent, safe on pre-existing DBs.
	addColumns := []string{
		`ALTER TABLE requests ADD COLUMN app_name         TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE requests ADD COLUMN user_name        TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE requests ADD COLUMN status_code      INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE requests ADD COLUMN retries          INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE requests ADD COLUMN upstream_url     TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE requests ADD COLUMN upstream_headers TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE requests ADD COLUMN upstream_body    TEXT NOT NULL DEFAULT ''`,
	}
	for _, stmt := range addColumns {
		if _, err := db.Exec(stmt); err != nil {
			// SQLite reports "duplicate column name" when the column already exists.
			if strings.Contains(err.Error(), "duplicate column name") {
				continue
			}
			return fmt.Errorf("add column: %w", err)
		}
	}

	if _, err := db.Exec(
		`CREATE INDEX IF NOT EXISTS idx_requests_app  ON requests(app_name);
		 CREATE INDEX IF NOT EXISTS idx_requests_user ON requests(user_name);`,
	); err != nil {
		return fmt.Errorf("attribution indexes: %w", err)
	}

	return seedPricing(db)
}

func seedPricing(db *sql.DB) error {
	rows := []struct {
		model, provider     string
		inputPer, outputPer float64
	}{
		// OpenAI
		{"gpt-4o", "openai", 0.0025, 0.010},
		{"gpt-4o-mini", "openai", 0.00015, 0.0006},
		{"gpt-4-turbo", "openai", 0.010, 0.030},
		{"gpt-4", "openai", 0.030, 0.060},
		{"gpt-3.5-turbo", "openai", 0.0005, 0.0015},
		{"o1", "openai", 0.015, 0.060},
		{"o1-mini", "openai", 0.003, 0.012},
		{"o3-mini", "openai", 0.0011, 0.0044},
		// Anthropic
		{"claude-opus-4-5", "anthropic", 0.015, 0.075},
		{"claude-sonnet-4-5", "anthropic", 0.003, 0.015},
		{"claude-haiku-4-5", "anthropic", 0.0008, 0.004},
		{"claude-3-opus-20240229", "anthropic", 0.015, 0.075},
		{"claude-3-5-sonnet-20241022", "anthropic", 0.003, 0.015},
		{"claude-3-5-haiku-20241022", "anthropic", 0.0008, 0.004},
		{"claude-3-haiku-20240307", "anthropic", 0.00025, 0.00125},
		// Gemini
		{"gemini-2.5-pro", "gemini", 0.00125, 0.010},
		{"gemini-2.5-flash", "gemini", 0.000075, 0.0003},
		{"gemini-1.5-pro", "gemini", 0.00125, 0.005},
		{"gemini-1.5-flash", "gemini", 0.000075, 0.0003},
		{"gemini-1.0-pro", "gemini", 0.0005, 0.0015},
	}

	stmt, err := db.Prepare(`
		INSERT OR IGNORE INTO model_pricing (model, provider, input_per_1k, output_per_1k, updated_at)
		VALUES (?, ?, ?, ?, 0)
	`)
	if err != nil {
		return fmt.Errorf("prepare seed: %w", err)
	}
	defer stmt.Close()

	for _, r := range rows {
		if _, err := stmt.Exec(r.model, r.provider, r.inputPer, r.outputPer); err != nil {
			return fmt.Errorf("seed %s: %w", r.model, err)
		}
	}
	return nil
}
