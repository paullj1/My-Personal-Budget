package database

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
)

// ApplyMigrations loads and executes the schema SQL on the current database.
func ApplyMigrations(ctx context.Context, db *sql.DB, path string) error {
	if path == "" {
		return fmt.Errorf("migration path is required")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read migrations: %w", err)
	}
	sqlText := sanitizeSchema(string(data))
	if strings.TrimSpace(sqlText) == "" {
		return nil
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration tx: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, sqlText); err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migrations: %w", err)
	}
	return nil
}

func sanitizeSchema(schema string) string {
	lines := strings.Split(schema, "\n")
	out := make([]string, 0, len(lines))
	skipCreateBlock := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, `\`) {
			continue
		}
		if strings.HasPrefix(trimmed, "SELECT 'CREATE DATABASE") {
			skipCreateBlock = true
			continue
		}
		if skipCreateBlock {
			if strings.Contains(trimmed, `\gexec`) {
				skipCreateBlock = false
			}
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}
