package db

import (
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

var sharedDB *sql.DB

func Init(path string) error {
	var err error
	sharedDB, err = sql.Open("sqlite", path)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}

	sharedDB.SetMaxOpenConns(1)
	sharedDB.SetMaxIdleConns(1)

	if _, err = sharedDB.Exec("PRAGMA journal_mode=WAL"); err != nil {
		return fmt.Errorf("enable WAL: %w", err)
	}
	if _, err = sharedDB.Exec("PRAGMA foreign_keys=ON"); err != nil {
		return fmt.Errorf("enable foreign keys: %w", err)
	}

	_, err = sharedDB.Exec(`
		CREATE TABLE IF NOT EXISTS jobs (
			id         TEXT PRIMARY KEY,
			status     TEXT NOT NULL,
			created_at TIMESTAMP NOT NULL,
			updated_at TIMESTAMP NOT NULL
		);

		CREATE TABLE IF NOT EXISTS stages (
			id            TEXT PRIMARY KEY,
			job_id        TEXT NOT NULL REFERENCES jobs(id),
			type          TEXT NOT NULL,
			status        TEXT NOT NULL,
			attempt_count INTEGER NOT NULL DEFAULT 0,
			worker_id     TEXT,
			input_path    TEXT,
			output_path   TEXT,
			error         TEXT,
			enqueued_at   TIMESTAMP,
			started_at    TIMESTAMP,
			updated_at    TIMESTAMP NOT NULL
		);

		CREATE INDEX IF NOT EXISTS idx_stages_job_id ON stages(job_id);
		CREATE INDEX IF NOT EXISTS idx_stages_status_updated ON stages(status, updated_at);
	`)
	if err != nil {
		return fmt.Errorf("create tables: %w", err)
	}

	return nil
}

func GetDB() *sql.DB {
	return sharedDB
}

func UpdateTimestamp(conn *sql.DB, table string, id string) error {
	query := fmt.Sprintf("UPDATE %s SET updated_at = ? WHERE id = ?", table)
	_, err := conn.Exec(query, time.Now().UTC(), id)
	return err
}
