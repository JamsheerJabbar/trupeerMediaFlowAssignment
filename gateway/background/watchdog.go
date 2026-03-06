package background

import (
	"context"
	"fmt"
	"log"
	"os"
	"strconv"
	"time"

	"mediaflow/gateway/db"
	"mediaflow/gateway/services"
)

func StartWatchdog() {
	interval, _ := strconv.Atoi(os.Getenv("WATCHDOG_INTERVAL_SECONDS"))
	if interval == 0 {
		interval = 60
	}
	timeoutOverlay, _ := strconv.Atoi(os.Getenv("TIMEOUT_OVERLAY"))
	if timeoutOverlay == 0 {
		timeoutOverlay = 1200
	}
	timeoutTranscode, _ := strconv.Atoi(os.Getenv("TIMEOUT_TRANSCODE"))
	if timeoutTranscode == 0 {
		timeoutTranscode = 900
	}
	timeoutExtract, _ := strconv.Atoi(os.Getenv("TIMEOUT_EXTRACT"))
	if timeoutExtract == 0 {
		timeoutExtract = 600
	}
	maxRetries, _ := strconv.Atoi(os.Getenv("MAX_RETRIES"))
	if maxRetries == 0 {
		maxRetries = 3
	}

	timeouts := map[string]int{
		"overlay":   timeoutOverlay,
		"transcode": timeoutTranscode,
		"extract":   timeoutExtract,
	}

	log.Printf("[WATCHDOG] Started — interval=%ds retries=%d timeouts=%v", interval, maxRetries, timeouts)

	for {
		time.Sleep(time.Duration(interval) * time.Second)
		func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("[WATCHDOG] Panic recovered: %v", r)
				}
			}()

			conn := db.GetDB()
			staleStages, err := services.GetStaleStages(conn, timeouts)
			if err != nil {
				log.Printf("[WATCHDOG] Error fetching stale stages: %v", err)
				return
			}

			ctx := context.Background()
			for _, stage := range staleStages {
				log.Printf("[WATCHDOG] Stale stage detected — id=%s type=%s last_updated=%s",
					stage.ID, stage.Type, stage.UpdatedAt.Format(time.RFC3339))

				newCount, err := services.IncrementAttemptCount(conn, stage.ID)
				if err != nil {
					log.Printf("[WATCHDOG] Error incrementing attempt for %s: %v", stage.ID, err)
					continue
				}

				if newCount <= maxRetries {
					_, err = services.UpdateStageStatus(conn, stage.ID, "pending", map[string]interface{}{
						"error": "watchdog_timeout_requeue",
					})
					if err != nil {
						log.Printf("[WATCHDOG] Error updating stage %s: %v", stage.ID, err)
						continue
					}
					extras := watchdogEnqueueExtras(stage)
					if err = services.EnqueueStage(ctx, stage.ID, stage.JobID, stage.Type, extras); err != nil {
						log.Printf("[WATCHDOG] Error re-enqueuing stage %s: %v", stage.ID, err)
						continue
					}
					log.Printf("[WATCHDOG] Re-queued stage %s — attempt %d of %d", stage.ID, newCount, maxRetries)
				} else {
					_, err = services.UpdateStageStatus(conn, stage.ID, "dead_letter", map[string]interface{}{
						"error": "max_retries_exceeded",
					})
					if err != nil {
						log.Printf("[WATCHDOG] Error dead-lettering stage %s: %v", stage.ID, err)
						continue
					}
					if err = services.PushToDLQ(ctx, stage.ID, stage.JobID, stage.Type, "max_retries_exceeded"); err != nil {
						log.Printf("[WATCHDOG] Error pushing stage %s to DLQ: %v", stage.ID, err)
						continue
					}
					log.Printf("[WATCHDOG] Stage %s dead-lettered after %d attempts", stage.ID, newCount)
				}
			}
		}()
	}
}

func watchdogEnqueueExtras(stage db.Stage) map[string]string {
	extras := map[string]string{}
	if stage.InputPath != nil {
		extras["input_path"] = *stage.InputPath
	}
	if stage.Type == "overlay" {
		extras["srt_path"] = fmt.Sprintf("jobs/%s/input/subtitles.srt", stage.JobID)
	}
	return extras
}
