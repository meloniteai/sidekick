package telemetry

import (
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strconv"
)

// Tables lists every telemetry table in a stable order. Used by the JSON dump
// and to validate a requested CSV table name.
var Tables = []string{
	"session",
	"session_heartbeat",
	"edit",
	"batch",
	"verifier_run",
	"finding",
}

// DumpJSON writes every table as one JSON object keyed by table name, each
// value an array of row objects. Pretty-printed so the result is eyeball-able
// straight from `sidekick export`.
func DumpJSON(db *sql.DB, w io.Writer) error {
	out := make(map[string][]map[string]any, len(Tables))
	for _, table := range Tables {
		rows, err := queryTable(db, table)
		if err != nil {
			return err
		}
		out[table] = rows
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

// DumpCSV writes a single table as CSV (header row + data rows) to w. The table
// name is validated against [Tables] so it can be interpolated into the query
// without risking injection.
func DumpCSV(db *sql.DB, table string, w io.Writer) error {
	if !validTable(table) {
		return fmt.Errorf("unknown table %q (want one of %v)", table, Tables)
	}
	rows, err := db.Query("SELECT * FROM " + table)
	if err != nil {
		return err
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return err
	}
	cw := csv.NewWriter(w)
	if err := cw.Write(cols); err != nil {
		return err
	}
	for rows.Next() {
		vals, err := scanRow(rows, len(cols))
		if err != nil {
			return err
		}
		rec := make([]string, len(cols))
		for i, v := range vals {
			rec[i] = csvCell(v)
		}
		if err := cw.Write(rec); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	cw.Flush()
	return cw.Error()
}

func queryTable(db *sql.DB, table string) ([]map[string]any, error) {
	if !validTable(table) {
		return nil, fmt.Errorf("unknown table %q", table)
	}
	rows, err := db.Query("SELECT * FROM " + table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for rows.Next() {
		vals, err := scanRow(rows, len(cols))
		if err != nil {
			return nil, err
		}
		m := make(map[string]any, len(cols))
		for i, c := range cols {
			m[c] = normalizeCell(vals[i])
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func scanRow(rows *sql.Rows, n int) ([]any, error) {
	vals := make([]any, n)
	ptrs := make([]any, n)
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	if err := rows.Scan(ptrs...); err != nil {
		return nil, err
	}
	return vals, nil
}

// normalizeCell converts driver-returned []byte to string so JSON output is
// readable text rather than base64-encoded bytes.
func normalizeCell(v any) any {
	if b, ok := v.([]byte); ok {
		return string(b)
	}
	return v
}

func csvCell(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case []byte:
		return string(x)
	case string:
		return x
	case int64:
		return strconv.FormatInt(x, 10)
	case float64:
		return strconv.FormatFloat(x, 'g', -1, 64)
	case bool:
		return strconv.FormatBool(x)
	default:
		return fmt.Sprintf("%v", x)
	}
}

func validTable(table string) bool {
	return slices.Contains(Tables, table)
}
