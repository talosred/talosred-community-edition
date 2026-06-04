package store

import "fmt"

// AttributionRow aggregates cost and usage for one app or user.
type AttributionRow struct {
	Key       string // app_name or user_name value ("" shown as "unattributed")
	Requests  int64
	InputTok  int64
	OutputTok int64
	CostUSD   float64
	AvgLatMs  int64
}

// AttributionByApp groups spend by the X-Talos-App header value.
func (s *Store) AttributionByApp() ([]*AttributionRow, error) {
	return s.attributionBy("app_name")
}

// AttributionByUser groups spend by the X-Talos-User header value.
func (s *Store) AttributionByUser() ([]*AttributionRow, error) {
	return s.attributionBy("user_name")
}

func (s *Store) attributionBy(column string) ([]*AttributionRow, error) {
	// column is not user-controlled — only "app_name" or "user_name" from callers above.
	query := fmt.Sprintf(`
		SELECT %s AS k,
		       COUNT(*),
		       COALESCE(SUM(input_tok), 0),
		       COALESCE(SUM(output_tok), 0),
		       COALESCE(SUM(cost_usd), 0),
		       COALESCE(CAST(AVG(latency_ms) AS INTEGER), 0)
		FROM requests
		GROUP BY %s
		ORDER BY SUM(cost_usd) DESC`, column, column)

	rows, err := s.db.Query(query)
	if err != nil {
		return nil, fmt.Errorf("attribution by %s: %w", column, err)
	}
	defer rows.Close()

	var out []*AttributionRow
	for rows.Next() {
		var a AttributionRow
		if err := rows.Scan(&a.Key, &a.Requests, &a.InputTok, &a.OutputTok, &a.CostUSD, &a.AvgLatMs); err != nil {
			return nil, fmt.Errorf("scan attribution: %w", err)
		}
		out = append(out, &a)
	}
	return out, rows.Err()
}
