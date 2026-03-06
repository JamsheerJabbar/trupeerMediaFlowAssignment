package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"mediaflow/workers/storage"
)

type ProcessFunc func(inputBytes []byte, stageID, jobID string, message map[string]string) ([]byte, error)

type Worker struct {
	WorkerID          string
	JobType           string
	RedisClient       *redis.Client
	GatewayURL        string
	MaxConcurrentJobs int
	HeartbeatInterval int
	HeartbeatTTL      int
	ProcessFn         ProcessFunc
	httpClient        *http.Client
}

func New(processFn ProcessFunc) (*Worker, error) {
	jobType := os.Getenv("JOB_TYPE")
	redisURL := os.Getenv("REDIS_URL")
	gatewayURL := os.Getenv("GATEWAY_URL")
	maxConcurrent, _ := strconv.Atoi(os.Getenv("MAX_CONCURRENT_JOBS"))
	if maxConcurrent == 0 {
		maxConcurrent = 2
	}
	hbInterval, _ := strconv.Atoi(os.Getenv("WORKER_HEARTBEAT_INTERVAL"))
	if hbInterval == 0 {
		hbInterval = 10
	}
	hbTTL, _ := strconv.Atoi(os.Getenv("WORKER_HEARTBEAT_TTL"))
	if hbTTL == 0 {
		hbTTL = 15
	}

	opt, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, fmt.Errorf("parse redis url: %w", err)
	}

	hostname, _ := os.Hostname()
	suffix := uuid.New().String()[:8]
	workerID := fmt.Sprintf("%s-%s-%s", jobType, hostname, suffix)

	if err := storage.Init(); err != nil {
		return nil, fmt.Errorf("init storage: %w", err)
	}

	return &Worker{
		WorkerID:          workerID,
		JobType:           jobType,
		RedisClient:       redis.NewClient(opt),
		GatewayURL:        gatewayURL,
		MaxConcurrentJobs: maxConcurrent,
		HeartbeatInterval: hbInterval,
		HeartbeatTTL:      hbTTL,
		ProcessFn:         processFn,
		httpClient:        &http.Client{Timeout: 30 * time.Second},
	}, nil
}

func (w *Worker) Run(ctx context.Context) {
	log.Printf("Worker %s starting — JOB_TYPE=%s concurrency=%d", w.WorkerID, w.JobType, w.MaxConcurrentJobs)

	done := make(chan struct{})

	go w.heartbeatLoop(ctx)

	for i := 0; i < w.MaxConcurrentJobs; i++ {
		go w.consumerLoop(ctx, i)
	}

	<-done
}

func (w *Worker) heartbeatLoop(ctx context.Context) {
	key := fmt.Sprintf("worker:presence:%s:%s", w.JobType, w.WorkerID)
	ttl := time.Duration(w.HeartbeatTTL) * time.Second

	for {
		func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("[%s] Heartbeat panic: %v", w.WorkerID, r)
				}
			}()
			if err := w.RedisClient.Set(ctx, key, w.WorkerID, ttl).Err(); err != nil {
				log.Printf("[%s] Heartbeat error: %v", w.WorkerID, err)
			}
		}()
		time.Sleep(time.Duration(w.HeartbeatInterval) * time.Second)
	}
}

func (w *Worker) consumerLoop(ctx context.Context, slot int) {
	queueKey := fmt.Sprintf("queue:%s", w.JobType)
	log.Printf("[%s] Consumer slot %d listening on %s", w.WorkerID, slot, queueKey)

	for {
		result, err := w.RedisClient.BRPop(ctx, 5*time.Second, queueKey).Result()
		if err == redis.Nil {
			continue
		}
		if err != nil {
			log.Printf("[%s] BRPOP error: %v", w.WorkerID, err)
			time.Sleep(time.Second)
			continue
		}

		var message map[string]string
		if err := json.Unmarshal([]byte(result[1]), &message); err != nil {
			log.Printf("[%s] Failed to parse message: %v", w.WorkerID, err)
			continue
		}

		stageID := message["stage_id"]
		log.Printf("[%s] Picked up stage %s", w.WorkerID, stageID)
		w.processJob(ctx, message)
	}
}

func (w *Worker) processJob(ctx context.Context, message map[string]string) {
	stageID := message["stage_id"]
	jobID := message["job_id"]

	// Step 1 — Report in_progress
	if err := w.patchStage(stageID, map[string]interface{}{
		"status":    "in_progress",
		"worker_id": w.WorkerID,
	}); err != nil {
		log.Printf("[%s] Failed to report in_progress for %s: %v", w.WorkerID, stageID, err)
		return
	}

	// Step 2 — Idempotency check
	ext := w.outputExtension(message)
	outputPath := fmt.Sprintf("jobs/%s/stages/%s/output.%s", jobID, stageID, ext)
	if storage.FileExists(ctx, outputPath) {
		log.Printf("[%s] Output already exists for stage %s, skipping", w.WorkerID, stageID)
		_ = w.patchStage(stageID, map[string]interface{}{
			"status":      "completed",
			"output_path": outputPath,
		})
		return
	}

	// Steps 3-6 wrapped with error handling
	func() {
		var reportErr error
		defer func() {
			if reportErr != nil {
				log.Printf("[%s] Stage %s failed: %v", w.WorkerID, stageID, reportErr)
				_ = w.patchStage(stageID, map[string]interface{}{
					"status": "failed",
					"error":  reportErr.Error(),
				})
			}
		}()

		// Step 3 — Fetch input
		inputPath, err := w.fetchInputPath(jobID, stageID)
		if err != nil {
			reportErr = fmt.Errorf("fetch input path: %w", err)
			return
		}
		inputBytes, err := storage.DownloadFile(ctx, inputPath)
		if err != nil {
			reportErr = fmt.Errorf("download input: %w", err)
			return
		}

		// Step 4 — Process
		outputBytes, err := w.ProcessFn(inputBytes, stageID, jobID, message)
		if err != nil {
			reportErr = fmt.Errorf("processing: %w", err)
			return
		}

		// Step 5 — Upload output
		mime := mimeForExtension(ext)
		if _, err = storage.UploadFile(ctx, outputBytes, outputPath, mime); err != nil {
			reportErr = fmt.Errorf("upload output: %w", err)
			return
		}

		// Step 6 — Report completed
		if err = w.patchStage(stageID, map[string]interface{}{
			"status":      "completed",
			"output_path": outputPath,
		}); err != nil {
			reportErr = fmt.Errorf("report completed: %w", err)
			return
		}

		log.Printf("[%s] Stage %s completed successfully", w.WorkerID, stageID)
	}()
}

func (w *Worker) outputExtension(message map[string]string) string {
	switch w.JobType {
	case "overlay":
		if ip, ok := message["input_path"]; ok {
			return strings.TrimPrefix(filepath.Ext(ip), ".")
		}
		return "mp4"
	case "transcode":
		return "mp4"
	case "extract":
		return "mp3"
	default:
		return "bin"
	}
}

func (w *Worker) patchStage(stageID string, body map[string]interface{}) error {
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	url := fmt.Sprintf("%s/internal/stages/%s", w.GatewayURL, stageID)
	req, err := http.NewRequest(http.MethodPatch, url, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := w.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	if resp.StatusCode >= 400 {
		return fmt.Errorf("gateway returned %d for PATCH stage %s", resp.StatusCode, stageID)
	}
	return nil
}

func (w *Worker) fetchInputPath(jobID, stageID string) (string, error) {
	url := fmt.Sprintf("%s/jobs/%s", w.GatewayURL, jobID)
	resp, err := w.httpClient.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("gateway returned %d for GET job %s", resp.StatusCode, jobID)
	}

	var jobResp struct {
		Stages []struct {
			ID        string  `json:"id"`
			InputPath *string `json:"input_path"`
		} `json:"stages"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&jobResp); err != nil {
		return "", fmt.Errorf("decode job response: %w", err)
	}

	for _, s := range jobResp.Stages {
		if s.ID == stageID && s.InputPath != nil {
			return *s.InputPath, nil
		}
	}
	return "", fmt.Errorf("stage %s not found in job %s", stageID, jobID)
}

func mimeForExtension(ext string) string {
	switch ext {
	case "mp4":
		return "video/mp4"
	case "mp3":
		return "audio/mpeg"
	case "avi":
		return "video/x-msvideo"
	case "mkv":
		return "video/x-matroska"
	case "mov":
		return "video/quicktime"
	case "webm":
		return "video/webm"
	default:
		return "application/octet-stream"
	}
}
