package db

import "time"

type Job struct {
	ID        string    `json:"id"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Stages    []Stage   `json:"stages,omitempty"`
}

type Stage struct {
	ID           string     `json:"id"`
	JobID        string     `json:"job_id"`
	Type         string     `json:"type"`
	Status       string     `json:"status"`
	AttemptCount int        `json:"attempt_count"`
	WorkerID     *string    `json:"worker_id,omitempty"`
	InputPath    *string    `json:"input_path,omitempty"`
	OutputPath   *string    `json:"output_path,omitempty"`
	Error        *string    `json:"error,omitempty"`
	EnqueuedAt   *time.Time `json:"enqueued_at,omitempty"`
	StartedAt    *time.Time `json:"started_at,omitempty"`
	UpdatedAt    time.Time  `json:"updated_at"`
	PresignedURL *string    `json:"presigned_url,omitempty"`
}
