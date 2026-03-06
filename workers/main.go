package main

import (
	"context"
	"log"
	"os"

	"mediaflow/workers/extract"
	"mediaflow/workers/overlay"
	"mediaflow/workers/transcode"
	"mediaflow/workers/worker"
)

func main() {
	jobType := os.Getenv("JOB_TYPE")
	if jobType == "" {
		log.Fatal("JOB_TYPE environment variable is required")
	}

	var processFn worker.ProcessFunc
	switch jobType {
	case "overlay":
		processFn = overlay.Process
	case "transcode":
		processFn = transcode.Process
	case "extract":
		processFn = extract.Process
	default:
		log.Fatalf("Unknown JOB_TYPE: %s", jobType)
	}

	w, err := worker.New(processFn)
	if err != nil {
		log.Fatalf("Failed to create worker: %v", err)
	}

	w.Run(context.Background())
}
