package routes

import (
	"log"
	"net/http"

	"github.com/go-chi/chi/v5"

	"mediaflow/gateway/services"
)

func RegisterAdminRoutes(r chi.Router) {
	r.Get("/admin/dlq/{stageType}", getDLQ)
	r.Get("/health", healthCheck)
}

func getDLQ(w http.ResponseWriter, r *http.Request) {
	stageType := chi.URLParam(r, "stageType")

	if stageType != "overlay" && stageType != "transcode" && stageType != "extract" {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{
			"error": "stage_type must be one of: overlay, transcode, extract",
		})
		return
	}

	messages, err := services.GetDLQMessages(r.Context(), stageType, 50)
	if err != nil {
		log.Printf("Failed to get DLQ for %s: %v", stageType, err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to read DLQ"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"stage_type": stageType,
		"count":      len(messages),
		"messages":   messages,
	})
}

func healthCheck(w http.ResponseWriter, r *http.Request) {
	client := services.GetRedisClient()
	err := client.Ping(r.Context()).Err()
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"status": "degraded",
			"redis":  "unreachable",
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"status": "ok",
		"redis":  "ok",
	})
}
