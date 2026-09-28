package service

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestGeneratedImageCleanupRemovesExpiredFile(t *testing.T) {
	root := t.TempDir()
	id := strings.Repeat("a", 32)
	path := filepath.Join(root, id+".png")
	if err := os.WriteFile(path, []byte("stale"), 0600); err != nil {
		t.Fatal(err)
	}
	repo := &archiveMemoryRepo{images: map[string]GeneratedImage{id: {ID: id, MIMEType: "image/png", ByteSize: 5, ExpiresAt: time.Now().Add(-time.Hour)}}, keys: map[string]bool{}}
	svc := NewGeneratedImageService(repo, root)
	defer svc.Stop()
	if err := svc.Cleanup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expired file remains: %v", err)
	}
	repo.mu.Lock()
	_, exists := repo.images[id]
	repo.mu.Unlock()
	if exists {
		t.Fatal("expired row remains")
	}
}
