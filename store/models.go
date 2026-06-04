package store

import (
	"database/sql"
	"fmt"
	"time"
)

type RequestLog struct {
	ID        string
	TS        time.Time
	Provider  string
	Model     string
	InputTok  int64
	OutputTok int64
	TTFTms    int64
	LatencyMs int64
	CostUSD   float64
	ReqJSON   string
	ResJSON   string
}

type Store struct {
	db *sql.DB
}

func New(db *sql.DB) *Store {
	return &Store{db: db}
}

func (s *Store) Insert(r *RequestLog) error {
	_, err := s.db.Exec(`
		INSERT INTO requests (id, ts, provider, model, input_tok, output_tok, ttft_ms, latency_ms, cost_usd, req_json, res_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID,
		r.TS.UnixMilli(),
		r.Provider,
		r.Model,
		r.InputTok,
		r.OutputTok,
		r.TTFTms,
		r.LatencyMs,
		r.CostUSD,
		r.ReqJSON,
		r.ResJSON,
	)
	if err != nil {
		return fmt.Errorf("insert request log: %w", err)
	}
	return nil
}

type ListFilter struct {
	Provider string
	Model    string
	Limit    int
	Offset   int
}

func (s *Store) List(f ListFilter) ([]*RequestLog, error) {
	if f.Limit == 0 {
		f.Limit = 100
	}

	query := `SELECT id, ts, provider, model, input_tok, output_tok, ttft_ms, latency_ms, cost_usd, req_json, res_json
	          FROM requests WHERE 1=1`
	args := []any{}

	if f.Provider != "" {
		query += " AND provider = ?"
		args = append(args, f.Provider)
	}
	if f.Model != "" {
		query += " AND model = ?"
		args = append(args, f.Model)
	}
	query += " ORDER BY ts DESC LIMIT ? OFFSET ?"
	args = append(args, f.Limit, f.Offset)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("list requests: %w", err)
	}
	defer rows.Close()

	var logs []*RequestLog
	for rows.Next() {
		var r RequestLog
		var tsMs int64
		if err := rows.Scan(&r.ID, &tsMs, &r.Provider, &r.Model, &r.InputTok, &r.OutputTok,
			&r.TTFTms, &r.LatencyMs, &r.CostUSD, &r.ReqJSON, &r.ResJSON); err != nil {
			return nil, fmt.Errorf("scan row: %w", err)
		}
		r.TS = time.UnixMilli(tsMs)
		logs = append(logs, &r)
	}
	return logs, rows.Err()
}

func (s *Store) Get(id string) (*RequestLog, error) {
	var r RequestLog
	var tsMs int64
	err := s.db.QueryRow(`
		SELECT id, ts, provider, model, input_tok, output_tok, ttft_ms, latency_ms, cost_usd, req_json, res_json
		FROM requests WHERE id = ?`, id).
		Scan(&r.ID, &tsMs, &r.Provider, &r.Model, &r.InputTok, &r.OutputTok,
			&r.TTFTms, &r.LatencyMs, &r.CostUSD, &r.ReqJSON, &r.ResJSON)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get request: %w", err)
	}
	r.TS = time.UnixMilli(tsMs)
	return &r, nil
}
