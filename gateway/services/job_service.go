package services

import (
	"database/sql"
	"time"

	"github.com/google/uuid"
	"mediaflow/gateway/db"
)

func CreateJob(conn *sql.DB, jobID string, stageTypes []string, inputPath string) (*db.Job, error) {
	tx, err := conn.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	now := time.Now().UTC()

	_, err = tx.Exec(
		"INSERT INTO jobs (id, status, created_at, updated_at) VALUES (?, ?, ?, ?)",
		jobID, "pending", now, now,
	)
	if err != nil {
		return nil, err
	}

	job := &db.Job{
		ID:        jobID,
		Status:    "pending",
		CreatedAt: now,
		UpdatedAt: now,
	}

	for _, st := range stageTypes {
		stageID := uuid.New().String()
		_, err = tx.Exec(
			`INSERT INTO stages (id, job_id, type, status, attempt_count, input_path, enqueued_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			stageID, jobID, st, "pending", 0, inputPath, now, now,
		)
		if err != nil {
			return nil, err
		}
		ip := inputPath
		enq := now
		job.Stages = append(job.Stages, db.Stage{
			ID:           stageID,
			JobID:        jobID,
			Type:         st,
			Status:       "pending",
			AttemptCount: 0,
			InputPath:    &ip,
			EnqueuedAt:   &enq,
			UpdatedAt:    now,
		})
	}

	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return job, nil
}

func GetJob(conn *sql.DB, jobID string) (*db.Job, error) {
	var job db.Job
	err := conn.QueryRow(
		"SELECT id, status, created_at, updated_at FROM jobs WHERE id = ?", jobID,
	).Scan(&job.ID, &job.Status, &job.CreatedAt, &job.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	rows, err := conn.Query(
		`SELECT id, job_id, type, status, attempt_count, worker_id, input_path,
		        output_path, error, enqueued_at, started_at, updated_at
		 FROM stages WHERE job_id = ?`, jobID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var s db.Stage
		if err := rows.Scan(
			&s.ID, &s.JobID, &s.Type, &s.Status, &s.AttemptCount,
			&s.WorkerID, &s.InputPath, &s.OutputPath, &s.Error,
			&s.EnqueuedAt, &s.StartedAt, &s.UpdatedAt,
		); err != nil {
			return nil, err
		}
		job.Stages = append(job.Stages, s)
	}

	job.Status = computeJobStatus(job.Stages)
	return &job, nil
}

func computeJobStatus(stages []db.Stage) string {
	if len(stages) == 0 {
		return "pending"
	}
	allCompleted := true
	for _, s := range stages {
		if s.Status == "dead_letter" {
			return "failed"
		}
		if s.Status != "completed" {
			allCompleted = false
		}
	}
	if allCompleted {
		return "completed"
	}
	for _, s := range stages {
		if s.Status == "in_progress" {
			return "in_progress"
		}
	}
	return "pending"
}

func GetStage(conn *sql.DB, stageID string) (*db.Stage, error) {
	var s db.Stage
	err := conn.QueryRow(
		`SELECT id, job_id, type, status, attempt_count, worker_id, input_path,
		        output_path, error, enqueued_at, started_at, updated_at
		 FROM stages WHERE id = ?`, stageID,
	).Scan(
		&s.ID, &s.JobID, &s.Type, &s.Status, &s.AttemptCount,
		&s.WorkerID, &s.InputPath, &s.OutputPath, &s.Error,
		&s.EnqueuedAt, &s.StartedAt, &s.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &s, nil
}

func UpdateStageStatus(conn *sql.DB, stageID, status string, opts map[string]interface{}) (*db.Stage, error) {
	now := time.Now().UTC()

	query := "UPDATE stages SET status = ?, updated_at = ?"
	args := []interface{}{status, now}

	if v, ok := opts["worker_id"]; ok {
		query += ", worker_id = ?"
		args = append(args, v)
	}
	if v, ok := opts["output_path"]; ok {
		query += ", output_path = ?"
		args = append(args, v)
	}
	if v, ok := opts["error"]; ok {
		query += ", error = ?"
		args = append(args, v)
	}
	if v, ok := opts["started_at"]; ok {
		query += ", started_at = ?"
		args = append(args, v)
	}

	query += " WHERE id = ?"
	args = append(args, stageID)

	if _, err := conn.Exec(query, args...); err != nil {
		return nil, err
	}

	stage, err := GetStage(conn, stageID)
	if err != nil {
		return nil, err
	}
	if stage != nil {
		_ = db.UpdateTimestamp(conn, "jobs", stage.JobID)
	}
	return stage, nil
}

func GetStaleStages(conn *sql.DB, timeouts map[string]int) ([]db.Stage, error) {
	var stages []db.Stage
	now := time.Now().UTC()

	for stageType, timeout := range timeouts {
		cutoff := now.Add(-time.Duration(timeout) * time.Second)
		rows, err := conn.Query(
			`SELECT id, job_id, type, status, attempt_count, worker_id, input_path,
			        output_path, error, enqueued_at, started_at, updated_at
			 FROM stages WHERE status = 'in_progress' AND type = ? AND updated_at < ?`,
			stageType, cutoff,
		)
		if err != nil {
			return nil, err
		}

		for rows.Next() {
			var s db.Stage
			if err := rows.Scan(
				&s.ID, &s.JobID, &s.Type, &s.Status, &s.AttemptCount,
				&s.WorkerID, &s.InputPath, &s.OutputPath, &s.Error,
				&s.EnqueuedAt, &s.StartedAt, &s.UpdatedAt,
			); err != nil {
				rows.Close()
				return nil, err
			}
			stages = append(stages, s)
		}
		rows.Close()
	}
	return stages, nil
}

func IncrementAttemptCount(conn *sql.DB, stageID string) (int, error) {
	if _, err := conn.Exec(
		"UPDATE stages SET attempt_count = attempt_count + 1 WHERE id = ?", stageID,
	); err != nil {
		return 0, err
	}

	var count int
	err := conn.QueryRow("SELECT attempt_count FROM stages WHERE id = ?", stageID).Scan(&count)
	return count, err
}

func ResetStageForRetry(conn *sql.DB, stageID string) (*db.Stage, error) {
	now := time.Now().UTC()
	if _, err := conn.Exec(
		`UPDATE stages SET status = 'pending', attempt_count = 0, error = NULL,
		 output_path = NULL, updated_at = ? WHERE id = ?`, now, stageID,
	); err != nil {
		return nil, err
	}
	return GetStage(conn, stageID)
}
