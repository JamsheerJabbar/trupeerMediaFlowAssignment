package background

import (
	"context"
	"log"
	"os"
	"strconv"
	"time"

	"mediaflow/gateway/services"
)

func StartQueueMonitor() {
	interval, _ := strconv.Atoi(os.Getenv("MONITOR_INTERVAL_SECONDS"))
	if interval == 0 {
		interval = 30
	}
	maxConcurrent, _ := strconv.Atoi(os.Getenv("MAX_CONCURRENT_JOBS"))
	if maxConcurrent == 0 {
		maxConcurrent = 2
	}
	threshold, _ := strconv.ParseFloat(os.Getenv("PRESSURE_THRESHOLD"), 64)
	if threshold == 0 {
		threshold = 0.8
	}

	log.Printf("[MONITOR] Started — interval=%ds threshold=%.0f%%", interval, threshold*100)

	jobTypes := []string{"overlay", "transcode", "extract"}

	for {
		time.Sleep(time.Duration(interval) * time.Second)
		func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("[MONITOR] Panic recovered: %v", r)
				}
			}()

			ctx := context.Background()
			for _, jt := range jobTypes {
				depth, err := services.GetQueueDepth(ctx, jt)
				if err != nil {
					log.Printf("[MONITOR] Error getting queue depth for %s: %v", jt, err)
					continue
				}
				workers, err := services.GetLiveWorkers(ctx, jt)
				if err != nil {
					log.Printf("[MONITOR] Error getting workers for %s: %v", jt, err)
					continue
				}

				liveWorkers := len(workers)
				capacity := liveWorkers * maxConcurrent

				if liveWorkers == 0 && depth > 0 {
					log.Printf("[MONITOR] CRITICAL — %s: %d jobs queued, zero workers alive", jt, depth)
				} else if capacity > 0 {
					utilization := float64(depth) / float64(capacity)
					if utilization > threshold {
						log.Printf("[MONITOR] WARNING — %s: queue=%d capacity=%d utilization=%.0f%%",
							jt, depth, capacity, utilization*100)
					} else {
						log.Printf("[MONITOR] INFO — %s: queue=%d workers=%d — healthy", jt, depth, liveWorkers)
					}
				} else {
					log.Printf("[MONITOR] INFO — %s: queue=%d workers=%d — healthy", jt, depth, liveWorkers)
				}
			}
		}()
	}
}
