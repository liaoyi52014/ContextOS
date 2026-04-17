package api

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const tempUploadTTL = time.Hour

type tempUploadMetadata struct {
	ID        string    `json:"id"`
	Path      string    `json:"path"`
	Filename  string    `json:"filename"`
	ExpiresAt time.Time `json:"expires_at"`
}

func tempUploadDir() string {
	return filepath.Join(os.TempDir(), "contextos_uploads")
}

func tempUploadMetaPath(id string) string {
	return filepath.Join(tempUploadDir(), id+".meta.json")
}

func saveTempUpload(meta *tempUploadMetadata) error {
	if meta == nil {
		return fmt.Errorf("temp upload metadata is required")
	}
	if err := os.MkdirAll(tempUploadDir(), 0o750); err != nil {
		return err
	}
	data, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	return os.WriteFile(tempUploadMetaPath(meta.ID), data, 0o600)
}

func loadTempUpload(id string) (*tempUploadMetadata, error) {
	data, err := os.ReadFile(tempUploadMetaPath(id))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var meta tempUploadMetadata
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, err
	}
	return &meta, nil
}

func cleanupExpiredTempUploads() error {
	entries, err := os.ReadDir(tempUploadDir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	now := time.Now()
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".meta.json") {
			continue
		}
		meta, err := loadTempUpload(strings.TrimSuffix(entry.Name(), ".meta.json"))
		if err != nil || meta == nil || meta.ExpiresAt.After(now) {
			continue
		}
		_ = os.Remove(meta.Path)
		_ = os.Remove(tempUploadMetaPath(meta.ID))
	}
	return nil
}

// StartTempUploadJanitor periodically removes expired temporary uploads until
// the context is canceled.
func StartTempUploadJanitor(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	_ = cleanupExpiredTempUploads()
	ticker := time.NewTicker(interval)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_ = cleanupExpiredTempUploads()
			}
		}
	}()
}
