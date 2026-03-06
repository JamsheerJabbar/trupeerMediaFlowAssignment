package transcode

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

const timeout = 900 * time.Second

func Process(inputBytes []byte, stageID, jobID string, message map[string]string) ([]byte, error) {
	tmpDir, err := os.MkdirTemp("", "transcode-*")
	if err != nil {
		return nil, fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	inputFile := filepath.Join(tmpDir, "input.mp4")
	outputFile := filepath.Join(tmpDir, "output.mp4")

	if err = os.WriteFile(inputFile, inputBytes, 0644); err != nil {
		return nil, fmt.Errorf("write input: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-y", "-i", inputFile,
		"-vf", "scale=-2:480",
		"-c:v", "libx264", "-crf", "23", "-preset", "fast",
		"-c:a", "aac", "-b:a", "128k",
		outputFile,
	)
	output, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return nil, fmt.Errorf("ffmpeg timeout after %v", timeout)
	}
	if err != nil {
		return nil, fmt.Errorf("ffmpeg failed: %s — %w", string(output), err)
	}

	return os.ReadFile(outputFile)
}
