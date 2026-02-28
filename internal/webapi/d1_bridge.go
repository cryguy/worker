package webapi

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cryguy/worker/v2/internal/core"

	// Pure-Go SQLite driver for database/sql (used by D1Bridge).
	_ "github.com/glebarez/sqlite"
)

// D1Bridge provides Go methods that back the D1 database JS bindings.
// Each D1 binding gets its own isolated SQLite database, completely
// separate from the application's main database.
type D1Bridge struct {
	DB         *sql.DB
	DatabaseID string
}

// Ensure D1Bridge implements core.D1Store.
var _ core.D1Store = (*D1Bridge)(nil)

// ValidateDatabaseID rejects database IDs that contain path traversal
// characters, null bytes, or are empty/too long.
func ValidateDatabaseID(id string) error {
	if id == "" {
		return fmt.Errorf("database ID must not be empty")
	}
	if len(id) > 128 {
		return fmt.Errorf("database ID too long")
	}
	if strings.Contains(id, "..") {
		return fmt.Errorf("database ID contains path traversal")
	}
	if strings.ContainsAny(id, "/\\") {
		return fmt.Errorf("database ID contains path separator")
	}
	if strings.ContainsRune(id, 0) {
		return fmt.Errorf("database ID contains null byte")
	}
	return nil
}

// OpenD1Database opens (or creates) an isolated SQLite database for the given
// database ID. The file is stored at {dataDir}/d1/{databaseID}.sqlite3.
func OpenD1Database(dataDir, databaseID string) (*D1Bridge, error) {
	if err := ValidateDatabaseID(databaseID); err != nil {
		return nil, err
	}
	d1Dir := filepath.Join(dataDir, "d1")
	if err := os.MkdirAll(d1Dir, 0755); err != nil {
		return nil, fmt.Errorf("creating D1 directory: %w", err)
	}
	dbPath := filepath.Join(d1Dir, databaseID+".sqlite3")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("opening D1 database %q: %w", databaseID, err)
	}
	// Enable WAL mode for better concurrent access.
	_, _ = db.Exec("PRAGMA journal_mode=WAL")
	return &D1Bridge{DB: db, DatabaseID: databaseID}, nil
}

// NewD1BridgeMemory creates an in-memory D1Bridge for testing.
func NewD1BridgeMemory(databaseID string) (*D1Bridge, error) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return nil, fmt.Errorf("opening in-memory D1 database: %w", err)
	}
	return &D1Bridge{DB: db, DatabaseID: databaseID}, nil
}

// Close closes the underlying database connection.
func (d *D1Bridge) Close() error {
	if d.DB != nil {
		return d.DB.Close()
	}
	return nil
}

// Exec runs a SQL statement with optional bindings and returns columns, rows, and metadata.
func (d *D1Bridge) Exec(sqlStr string, bindings []interface{}) (*core.D1ExecResult, error) {
	upperSQL := strings.TrimSpace(strings.ToUpper(sqlStr))

	// Block dangerous SQL commands that could escape the D1 sandbox.
	for _, blocked := range []string{"ATTACH", "DETACH"} {
		if strings.HasPrefix(upperSQL, blocked) {
			return nil, fmt.Errorf("D1: %s statements are not allowed", blocked)
		}
	}

	// Block dangerous PRAGMAs (allow only safe introspection ones).
	if strings.HasPrefix(upperSQL, "PRAGMA") {
		allowed := []string{"PRAGMA TABLE_INFO", "PRAGMA TABLE_LIST", "PRAGMA INDEX_LIST",
			"PRAGMA INDEX_INFO", "PRAGMA FOREIGN_KEY_LIST", "PRAGMA JOURNAL_MODE"}
		isAllowed := false
		for _, a := range allowed {
			if strings.HasPrefix(upperSQL, a) {
				isAllowed = true
				break
			}
		}
		if !isAllowed {
			return nil, fmt.Errorf("D1: this PRAGMA is not allowed")
		}
	}

	isQuery := strings.HasPrefix(upperSQL, "SELECT") ||
		strings.HasPrefix(upperSQL, "PRAGMA") ||
		strings.HasPrefix(upperSQL, "WITH")

	if isQuery {
		rows, err := d.DB.Query(sqlStr, bindings...)
		if err != nil {
			return nil, fmt.Errorf("D1: query error: %w", err)
		}
		defer func() { _ = rows.Close() }()

		columns, err := rows.Columns()
		if err != nil {
			return nil, fmt.Errorf("D1: columns error: %w", err)
		}

		var resultRows [][]interface{}
		for rows.Next() {
			values := make([]interface{}, len(columns))
			valuePtrs := make([]interface{}, len(columns))
			for i := range values {
				valuePtrs[i] = &values[i]
			}
			if err := rows.Scan(valuePtrs...); err != nil {
				return nil, fmt.Errorf("D1: scan error: %w", err)
			}
			row := make([]interface{}, len(columns))
			for i, v := range values {
				if b, ok := v.([]byte); ok {
					row[i] = string(b)
				} else {
					row[i] = v
				}
			}
			resultRows = append(resultRows, row)
		}
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("D1: rows iteration error: %w", err)
		}

		return &core.D1ExecResult{
			Columns: columns,
			Rows:    resultRows,
			Meta: core.D1Meta{
				ChangedDB: false,
				RowsRead:  len(resultRows),
			},
		}, nil
	}

	// Non-query (INSERT, UPDATE, DELETE, CREATE, DROP, etc.)
	result, err := d.DB.Exec(sqlStr, bindings...)
	if err != nil {
		return nil, fmt.Errorf("D1: exec error: %w", err)
	}

	changes, _ := result.RowsAffected()
	lastID, _ := result.LastInsertId()

	return &core.D1ExecResult{
		Columns: []string{},
		Rows:    [][]interface{}{},
		Meta: core.D1Meta{
			ChangedDB:   changes > 0,
			Changes:     changes,
			LastRowID:   lastID,
			RowsWritten: int(changes),
		},
	}, nil
}
