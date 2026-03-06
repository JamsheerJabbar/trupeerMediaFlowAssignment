package routes

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"mediaflow/gateway/db"
	"mediaflow/gateway/services"
)

func RegisterInternalRoutes(r chi.Router) {
	r.Patch("/internal/stages/{stageID}", updateStage)
}

type updateStageRequest struct {
	Status     string  `json:"status"`
	WorkerID   *string `json:"worker_id,omitempty"`
	OutputPath *string `json:"output_path,omitempty"`
	Error      *string `json:"error,omitempty"`
}

func updateStage(w http.ResponseWriter, r *http.Request) {
	stageID := chi.URLParam(r, "stageID")

	var req updateStageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}

	conn := db.GetDB()

	switch req.Status {
	case "in_progress":
		opts := map[string]interface{}{
			"started_at": time.Now().UTC(),
		}
		if req.WorkerID != nil {
			opts["worker_id"] = *req.WorkerID
		}
		stage, err := services.UpdateStageStatus(conn, stageID, "in_progress", opts)
		if err != nil {
			log.Printf("Failed to update stage %s to in_progress: %v", stageID, err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "update failed"})
			return
		}
		writeJSON(w, http.StatusOK, stage)

	case "completed":
		opts := map[string]interface{}{}
		if req.OutputPath != nil {
			opts["output_path"] = *req.OutputPath
		}
		stage, err := services.UpdateStageStatus(conn, stageID, "completed", opts)
		if err != nil {
			log.Printf("Failed to update stage %s to completed: %v", stageID, err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "update failed"})
			return
		}
		writeJSON(w, http.StatusOK, stage)

	case "failed":
		newCount, err := services.IncrementAttemptCount(conn, stageID)
		if err != nil {
			log.Printf("Failed to increment attempt for stage %s: %v", stageID, err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "increment failed"})
			return
		}

		maxRetries, _ := strconv.Atoi(os.Getenv("MAX_RETRIES"))
		if maxRetries == 0 {
			maxRetries = 3
		}

		stage, err := services.GetStage(conn, stageID)
		if err != nil || stage == nil {
			log.Printf("Failed to get stage %s: %v", stageID, err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "stage lookup failed"})
			return
		}

		ctx := context.Background()
		errorMsg := ""
		if req.Error != nil {
			errorMsg = *req.Error
		}

		if newCount <= maxRetries {
			_, err = services.UpdateStageStatus(conn, stageID, "pending", map[string]interface{}{
				"error": errorMsg,
			})
			if err != nil {
				log.Printf("Failed to requeue stage %s: %v", stageID, err)
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "requeue failed"})
				return
			}
			extras := stageEnqueueExtras(stage)
			if err = services.EnqueueStage(ctx, stage.ID, stage.JobID, stage.Type, extras); err != nil {
				log.Printf("Failed to enqueue stage %s: %v", stageID, err)
			}
			writeJSON(w, http.StatusOK, map[string]interface{}{
				"action":  "requeued",
				"attempt": newCount,
			})
		} else {
			_, err = services.UpdateStageStatus(conn, stageID, "dead_letter", map[string]interface{}{
				"error": errorMsg,
			})
			if err != nil {
				log.Printf("Failed to dead-letter stage %s: %v", stageID, err)
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "dead letter failed"})
				return
			}
			if err = services.PushToDLQ(ctx, stage.ID, stage.JobID, stage.Type, errorMsg); err != nil {
				log.Printf("Failed to push stage %s to DLQ: %v", stageID, err)
			}
			writeJSON(w, http.StatusOK, map[string]interface{}{
				"action":  "dead_lettered",
				"attempt": newCount,
			})
		}

	default:
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{
			"error": "status must be one of: in_progress, completed, failed",
		})
	}
}

func stageEnqueueExtras(stage *db.Stage) map[string]string {
	extras := map[string]string{}
	if stage.InputPath != nil {
		extras["input_path"] = *stage.InputPath
	}
	if stage.Type == "overlay" {
		extras["srt_path"] = fmt.Sprintf("jobs/%s/input/subtitles.srt", stage.JobID)
	}
	return extras
}
