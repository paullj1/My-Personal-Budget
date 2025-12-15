package database

import (
	"database/sql"
	"fmt"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// Connect opens a Postgres connection using pgx and applies basic pool settings.
// It will retry up to retries times with the given interval between attempts.
func Connect(dbURL string, retries int, interval time.Duration) (*sql.DB, error) {
	if dbURL == "" {
		return nil, fmt.Errorf("DATABASE_URL is required")
	}
	if retries < 1 {
		retries = 1
	}
	if interval <= 0 {
		interval = 500 * time.Millisecond
	}

	db, err := sql.Open("pgx", dbURL)
	if err != nil {
		return nil, err
	}

	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxIdleTime(10 * time.Minute)
	db.SetConnMaxLifetime(60 * time.Minute)

	var pingErr error
	for i := 0; i < retries; i++ {
		pingErr = db.Ping()
		if pingErr == nil {
			return db, nil
		}
		time.Sleep(interval)
	}

	return nil, fmt.Errorf("failed to connect to db after %d attempts: %w", retries, pingErr)
}
