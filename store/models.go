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

	// Attribution (Pain 1)
	AppName  string
	UserName string

	// Debug / reproduction (Pain 3)
	StatusCode      int
	Retries         int
	UpstreamURL     string
	UpstreamHeaders string // redacted JSON object
	UpstreamBody    string // exact translated body sent upstream
}

const requestColumns = `id, ts, provider, model, input_tok, output_tok, ttft_ms, latency_ms,
	cost_usd, req_json, res_json, app_name, user_name, status_code, retries,
	upstream_url, upstream_headers, upstream_body`

type Store struct {
	db          *sql.DB
	broadcaster *Broadcaster
}

func New(db *sql.DB, b *Broadcaster) *Store {
	return &Store{db: db, broadcaster: b}
}

func (s *Store) Insert(r *RequestLog) error {
	_, err := s.db.Exec(`
		INSERT INTO requests (`+requestColumns+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
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
		r.AppName,
		r.UserName,
		r.StatusCode,
		r.Retries,
		r.UpstreamURL,
		r.UpstreamHeaders,
		r.UpstreamBody,
	)
	if err != nil {
		return fmt.Errorf("insert request log: %w", err)
	}
	if s.broadcaster != nil {
		s.broadcaster.Publish(r)
	}
	return nil
}

func scanRequest(sc interface {
	Scan(...any) error
}) (*RequestLog, error) {
	var r RequestLog
	var tsMs int64
	if err := sc.Scan(&r.ID, &tsMs, &r.Provider, &r.Model, &r.InputTok, &r.OutputTok,
		&r.TTFTms, &r.LatencyMs, &r.CostUSD, &r.ReqJSON, &r.ResJSON,
		&r.AppName, &r.UserName, &r.StatusCode, &r.Retries,
		&r.UpstreamURL, &r.UpstreamHeaders, &r.UpstreamBody); err != nil {
		return nil, err
	}
	r.TS = time.UnixMilli(tsMs)
	return &r, nil
}

type ListFilter struct {
	Provider string
	Model    string
	App      string
	User     string
	Limit    int
	Offset   int
}

func (s *Store) List(f ListFilter) ([]*RequestLog, error) {
	if f.Limit == 0 {
		f.Limit = 100
	}

	query := `SELECT ` + requestColumns + ` FROM requests WHERE 1=1`
	args := []any{}

	if f.Provider != "" {
		query += " AND provider = ?"
		args = append(args, f.Provider)
	}
	if f.Model != "" {
		query += " AND model = ?"
		args = append(args, f.Model)
	}
	if f.App != "" {
		query += " AND app_name = ?"
		args = append(args, f.App)
	}
	if f.User != "" {
		query += " AND user_name = ?"
		args = append(args, f.User)
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
		r, err := scanRequest(rows)
		if err != nil {
			return nil, fmt.Errorf("scan row: %w", err)
		}
		logs = append(logs, r)
	}
	return logs, rows.Err()
}

func (s *Store) Get(id string) (*RequestLog, error) {
	row := s.db.QueryRow(`SELECT `+requestColumns+` FROM requests WHERE id = ?`, id)
	r, err := scanRequest(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get request: %w", err)
	}
	return r, nil
}
