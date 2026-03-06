package routes

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"mediaflow/gateway/db"
	"mediaflow/gateway/services"
)

var validStageTypes = map[string]bool{
	"overlay":   true,
	"transcode": true,
	"extract":   true,
}

func RegisterJobRoutes(r chi.Router) {
	r.Post("/jobs", createJob)
	r.Get("/jobs/{jobID}", getJob)
	r.Post("/jobs/{jobID}/retry", retryJob)
}

func createJob(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(200 << 20); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid multipart form: " + err.Error()})
		return
	}

	stagesStr := r.FormValue("stages")
	if stagesStr == "" {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "stages field is required"})
		return
	}

	stageTypes := strings.Split(stagesStr, ",")
	for i, st := range stageTypes {
		stageTypes[i] = strings.TrimSpace(st)
		if !validStageTypes[stageTypes[i]] {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{
				"error": fmt.Sprintf("invalid stage type: %q — must be one of [overlay, transcode, extract]", stageTypes[i]),
			})
			return
		}
	}

	hasOverlay := false
	for _, st := range stageTypes {
		if st == "overlay" {
			hasOverlay = true
			break
		}
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "file field is required"})
		return
	}
	defer file.Close()

	var srtBytes []byte
	if hasOverlay {
		srtFile, _, srtErr := r.FormFile("srt_file")
		if srtErr != nil {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "srt_file is required for overlay stage"})
			return
		}
		defer srtFile.Close()
		srtBytes, err = io.ReadAll(srtFile)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to read srt_file"})
			return
		}
	}

	ctx := r.Context()

	pfErr := services.PreflightCheck(ctx, stageTypes)
	if pfErr != nil {
		if pfErr.Error == "no_workers_available" {
			writeJSON(w, http.StatusServiceUnavailable, pfErr)
			return
		}
		if pfErr.Error == "queue_at_capacity" {
			w.Header().Set("Retry-After", "30")
			writeJSON(w, http.StatusTooManyRequests, pfErr)
			return
		}
	}

	jobID := uuid.New().String()

	fileBytes, err := io.ReadAll(file)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to read file"})
		return
	}

	inputPath := services.InputPathBuilder(jobID, header.Filename)
	contentType := header.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	if _, err = services.UploadFile(ctx, fileBytes, inputPath, contentType); err != nil {
		log.Printf("Failed to upload input file: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to upload file"})
		return
	}

	var srtPath string
	if len(srtBytes) > 0 {
		srtPath = fmt.Sprintf("jobs/%s/input/subtitles.srt", jobID)
		if _, err = services.UploadFile(ctx, srtBytes, srtPath, "text/plain"); err != nil {
			log.Printf("Failed to upload SRT file: %v", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to upload srt file"})
			return
		}
	}

	conn := db.GetDB()
	job, err := services.CreateJob(conn, jobID, stageTypes, inputPath)
	if err != nil {
		log.Printf("Failed to create job: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to create job"})
		return
	}

	for _, stage := range job.Stages {
		extras := map[string]string{
			"input_path": inputPath,
		}
		if stage.Type == "overlay" && srtPath != "" {
			extras["srt_path"] = srtPath
		}
		if err = services.EnqueueStage(ctx, stage.ID, jobID, stage.Type, extras); err != nil {
			log.Printf("Failed to enqueue stage %s: %v", stage.ID, err)
		}
	}

	writeJSON(w, http.StatusAccepted, job)
}

func getJob(w http.ResponseWriter, r *http.Request) {
	jobID := chi.URLParam(r, "jobID")
	conn := db.GetDB()

	job, err := services.GetJob(conn, jobID)
	if err != nil {
		log.Printf("Failed to get job %s: %v", jobID, err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	if job == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "job not found"})
		return
	}

	ctx := context.Background()
	for i := range job.Stages {
		s := &job.Stages[i]
		if s.Status == "completed" && s.OutputPath != nil {
			url, err := services.GetPresignedURL(ctx, *s.OutputPath, 24*time.Hour)
			if err == nil {
				s.PresignedURL = &url
			}
		}
	}

	writeJSON(w, http.StatusOK, job)
}

func retryJob(w http.ResponseWriter, r *http.Request) {
	jobID := chi.URLParam(r, "jobID")
	conn := db.GetDB()

	job, err := services.GetJob(conn, jobID)
	if err != nil {
		log.Printf("Failed to get job %s: %v", jobID, err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	if job == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "job not found"})
		return
	}

	ctx := r.Context()
	for _, stage := range job.Stages {
		if stage.Status == "failed" || stage.Status == "dead_letter" {
			if _, err = services.ResetStageForRetry(conn, stage.ID); err != nil {
				log.Printf("Failed to reset stage %s: %v", stage.ID, err)
				continue
			}
			extras := buildEnqueueExtras(stage, job.ID)
			if err = services.EnqueueStage(ctx, stage.ID, job.ID, stage.Type, extras); err != nil {
				log.Printf("Failed to enqueue stage %s: %v", stage.ID, err)
			}
		}
	}

	updatedJob, err := services.GetJob(conn, jobID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	writeJSON(w, http.StatusAccepted, updatedJob)
}

func buildEnqueueExtras(stage db.Stage, jobID string) map[string]string {
	extras := map[string]string{}
	if stage.InputPath != nil {
		extras["input_path"] = *stage.InputPath
	}
	if stage.Type == "overlay" {
		extras["srt_path"] = fmt.Sprintf("jobs/%s/input/subtitles.srt", jobID)
	}
	return extras
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
