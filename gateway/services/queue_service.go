package services

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"

	"github.com/redis/go-redis/v9"
)

var (
	redisClient        *redis.Client
	maxConcurrentJobs  int
	pressureThreshold  float64
	workerHeartbeatTTL int
)

func InitQueue() error {
	redisURL := os.Getenv("REDIS_URL")
	opt, err := redis.ParseURL(redisURL)
	if err != nil {
		return fmt.Errorf("parse redis url: %w", err)
	}
	redisClient = redis.NewClient(opt)

	maxConcurrentJobs, _ = strconv.Atoi(os.Getenv("MAX_CONCURRENT_JOBS"))
	if maxConcurrentJobs == 0 {
		maxConcurrentJobs = 2
	}

	pt, _ := strconv.ParseFloat(os.Getenv("PRESSURE_THRESHOLD"), 64)
	if pt == 0 {
		pt = 0.8
	}
	pressureThreshold = pt

	workerHeartbeatTTL, _ = strconv.Atoi(os.Getenv("WORKER_HEARTBEAT_TTL"))
	if workerHeartbeatTTL == 0 {
		workerHeartbeatTTL = 15
	}

	return redisClient.Ping(context.Background()).Err()
}

func GetRedisClient() *redis.Client {
	return redisClient
}

func EnqueueStage(ctx context.Context, stageID, jobID, stageType string, extras map[string]string) error {
	msg := map[string]string{
		"stage_id": stageID,
		"job_id":   jobID,
		"type":     stageType,
	}
	for k, v := range extras {
		msg[k] = v
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	return redisClient.LPush(ctx, fmt.Sprintf("queue:%s", stageType), string(data)).Err()
}

func PushToDLQ(ctx context.Context, stageID, jobID, stageType, errorMsg string) error {
	msg := map[string]string{
		"stage_id": stageID,
		"job_id":   jobID,
		"type":     stageType,
		"error":    errorMsg,
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	return redisClient.LPush(ctx, fmt.Sprintf("dlq:%s", stageType), string(data)).Err()
}

func GetDLQMessages(ctx context.Context, stageType string, maxCount int64) ([]map[string]interface{}, error) {
	if maxCount == 0 {
		maxCount = 50
	}
	items, err := redisClient.LRange(ctx, fmt.Sprintf("dlq:%s", stageType), 0, maxCount-1).Result()
	if err != nil {
		return nil, err
	}
	messages := make([]map[string]interface{}, 0, len(items))
	for _, item := range items {
		var msg map[string]interface{}
		if err := json.Unmarshal([]byte(item), &msg); err == nil {
			messages = append(messages, msg)
		}
	}
	return messages, nil
}

func GetQueueDepth(ctx context.Context, stageType string) (int64, error) {
	return redisClient.LLen(ctx, fmt.Sprintf("queue:%s", stageType)).Result()
}

func GetLiveWorkers(ctx context.Context, stageType string) ([]string, error) {
	return redisClient.Keys(ctx, fmt.Sprintf("worker:presence:%s:*", stageType)).Result()
}

type PreflightError struct {
	Error      string `json:"error"`
	Stage      string `json:"stage"`
	RetryAfter int    `json:"retry_after,omitempty"`
}

func PreflightCheck(ctx context.Context, stageTypes []string) *PreflightError {
	for _, st := range stageTypes {
		workers, err := GetLiveWorkers(ctx, st)
		if err != nil {
			continue
		}
		depth, err := GetQueueDepth(ctx, st)
		if err != nil {
			continue
		}

		liveCount := len(workers)
		capacity := liveCount * maxConcurrentJobs

		if liveCount == 0 {
			return &PreflightError{Error: "no_workers_available", Stage: st}
		}

		if capacity > 0 {
			utilization := float64(depth) / float64(capacity)
			if utilization > pressureThreshold {
				return &PreflightError{Error: "queue_at_capacity", Stage: st, RetryAfter: 30}
			}
		}
	}
	return nil
}
