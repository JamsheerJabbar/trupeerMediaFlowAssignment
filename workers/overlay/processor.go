package overlay

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"mediaflow/workers/storage"
)

const timeout = 1200 * time.Second

func Process(inputBytes []byte, stageID, jobID string, message map[string]string) ([]byte, error) {
	srtPath := message["srt_path"]
	if srtPath == "" {
		return nil, fmt.Errorf("srt_path missing in message for overlay stage %s", stageID)
	}

	inputPath := message["input_path"]
	ext := strings.TrimPrefix(filepath.Ext(inputPath), ".")
	if ext == "" {
		ext = "mp4"
	}

	srtBytes, err := storage.DownloadFile(context.Background(), srtPath)
	if err != nil {
		return nil, fmt.Errorf("download srt: %w", err)
	}

	tmpDir, err := os.MkdirTemp("", "overlay-*")
	if err != nil {
		return nil, fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	inputFile := filepath.Join(tmpDir, "input."+ext)
	srtFile := filepath.Join(tmpDir, "subtitles.srt")
	outputFile := filepath.Join(tmpDir, "output."+ext)

	if err = os.WriteFile(inputFile, inputBytes, 0644); err != nil {
		return nil, fmt.Errorf("write input: %w", err)
	}
	if err = os.WriteFile(srtFile, srtBytes, 0644); err != nil {
		return nil, fmt.Errorf("write srt: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-y", "-i", inputFile,
		"-vf", fmt.Sprintf("subtitles=%s", srtFile),
		"-c:a", "copy",
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
