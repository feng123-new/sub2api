package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestGeneratedImageAdmissionReservesBeforeCopy(t *testing.T) {
	s := &GeneratedImageService{
		repo: &archiveMemoryRepo{}, jobs: make(chan generatedImageJob, 2),
		slots: make(chan struct{}, 3), done: make(chan struct{}),
	}
	for i := 0; i < cap(s.slots); i++ {
		s.slots <- struct{}{}
	}
	payload := []byte(`{"data":[{"b64_json":"aGVsbG8="}]}`)
	allocations := testing.AllocsPerRun(100, func() {
		if s.Submit(GeneratedImageOwner{UserID: 1, RequestID: "full"}, payload) {
			t.Fatal("admitted an image beyond the in-flight memory bound")
		}
	})
	if allocations != 0 {
		t.Fatalf("rejected capture allocated payload storage: %v", allocations)
	}
}

func TestGeneratedImageAdditionalFinalEnvelopes(t *testing.T) {
	b64 := tinyArchivePNG(t)
	for _, payload := range []string{
		`{"type":"response","output":[{"type":"image_generation_call","status":"completed","result":"` + b64 + `"}]}`,
		`{"type":"response.done","response":{"output":[{"type":"image_generation_call","result":"` + b64 + `"}]}}`,
		`{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"` + b64 + `"}}]}}]}`,
		`{"response":{"candidates":[{"content":{"parts":[{"inline_data":{"mime_type":"image/png","data":"` + b64 + `"}}]}}]}}`,
	} {
		if got := len(parseGeneratedImages([]byte(payload))); got != 1 {
			t.Fatalf("final image envelope not captured: %s", payload[:40])
		}
	}
	sources := parseGeneratedImages([]byte(`{"data":[{"url":"data:image/png;base64,` + b64 + `"}]}`))
	if len(sources) != 1 || sources[0].b64 == "" || sources[0].url != "" {
		t.Fatal("inline image URL must decode locally without a network request")
	}
}

func TestGeneratedImageRejectsNonPublicNetworks(t *testing.T) {
	for _, value := range []string{"100.64.0.1", "198.18.0.1", "192.0.2.1", "198.51.100.1", "203.0.113.1", "240.0.0.1", "2001:db8::1", "64:ff9b::a00:1"} {
		if validGeneratedImageIP(net.ParseIP(value)) {
			t.Errorf("accepted special-use address %s", value)
		}
	}
}

func TestGeneratedImageDownloadCannotReachLoopback(t *testing.T) {
	server := httptest.NewServer(nil)
	defer server.Close()
	s := &GeneratedImageService{client: newGeneratedImageHTTPClient()}
	if _, err := s.download(context.Background(), server.URL); err == nil {
		t.Fatal("archive downloader reached a private address")
	}
}

type archiveMemoryRepo struct {
	mu     sync.Mutex
	images map[string]GeneratedImage
	keys   map[string]bool
}

func (r *archiveMemoryRepo) Insert(_ context.Context, v GeneratedImage) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	k := string(rune(v.UserID)) + v.RequestID + v.SHA256
	if r.keys[k] {
		return false, nil
	}
	r.keys[k] = true
	r.images[v.ID] = v
	return true, nil
}
func (r *archiveMemoryRepo) List(context.Context, GeneratedImageFilter) (GeneratedImageList, error) {
	return GeneratedImageList{}, nil
}
func (r *archiveMemoryRepo) Get(_ context.Context, id string) (GeneratedImage, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.images[id]
	if !ok {
		return v, ErrGeneratedImageNotFound
	}
	return v, nil
}
func (r *archiveMemoryRepo) Delete(_ context.Context, id string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.images[id]
	delete(r.images, id)
	return ok, nil
}
func (r *archiveMemoryRepo) Prune(_ context.Context, now time.Time, maxBytes int64) ([]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var paths []string
	for k, v := range r.images {
		if !v.ExpiresAt.After(now) {
			paths = append(paths, k)
			delete(r.images, k)
		}
	}
	return paths, nil
}
func (r *archiveMemoryRepo) HasID(_ context.Context, id string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.images[id]
	return ok, nil
}

func tinyArchivePNG(t *testing.T) string {
	t.Helper()
	var b bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 2, 3))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(b.Bytes())
}
func TestGeneratedImageFinalEnvelopes(t *testing.T) {
	b64 := tinyArchivePNG(t)
	cases := []struct {
		name    string
		payload any
		count   int
	}{
		{"images", map[string]any{"data": []any{map[string]any{"b64_json": b64}, map[string]any{"url": "https://example.org/result.png"}}}, 2},
		{"responses", map[string]any{"output": []any{map[string]any{"type": "image_generation_call", "status": "completed", "result": b64}}}, 1},
		{"output done", map[string]any{"type": "response.output_item.done", "item": map[string]any{"type": "image_generation_call", "result": b64}}, 1},
		{"response completed", map[string]any{"type": "response.completed", "response": map[string]any{"output": []any{map[string]any{"type": "image_generation_call", "result": b64}}}}, 1},
		{"direct completed", map[string]any{"type": "image_generation.completed", "b64_json": b64}, 1},
		{"direct edit", map[string]any{"type": "image_edit.completed", "data": []any{map[string]any{"b64_json": b64}}}, 1},
		{"partial", map[string]any{"type": "image_generation.partial_image", "b64_json": b64}, 0},
		{"incomplete", map[string]any{"output": []any{map[string]any{"type": "image_generation_call", "status": "in_progress", "result": b64}}}, 0},
		{"input echo", map[string]any{"input": []any{map[string]any{"image_url": "https://example.org/input.png"}}}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b, _ := json.Marshal(tc.payload)
			if got := len(parseGeneratedImages(b)); got != tc.count {
				t.Fatalf("got %d, want %d", got, tc.count)
			}
		})
	}
}
func TestGeneratedImageSSEFinalOnly(t *testing.T) {
	b64 := tinyArchivePNG(t)
	payload := "data: {\"type\":\"image_generation.partial_image\",\"b64_json\":\"" + b64 + "\"}\n\ndata: {\"type\":\"image_generation.completed\",\"b64_json\":\"" + b64 + "\"}\n\n"
	if got := len(parseGeneratedImages([]byte(payload))); got != 1 {
		t.Fatalf("got %d", got)
	}
}
func TestGeneratedImageRejectsPrivateIP(t *testing.T) {
	for _, s := range []string{"127.0.0.1", "10.1.2.3", "169.254.169.254", "::1", "fc00::1", "::ffff:127.0.0.1", "0.0.0.0"} {
		if validGeneratedImageIP(net.ParseIP(s)) {
			t.Errorf("accepted %s", s)
		}
	}
	if !validGeneratedImageIP(net.ParseIP("1.1.1.1")) {
		t.Fatal("public rejected")
	}
}
func TestGeneratedImageArchiveDedupAndAccess(t *testing.T) {
	root := t.TempDir()
	repo := &archiveMemoryRepo{images: map[string]GeneratedImage{}, keys: map[string]bool{}}
	s := NewGeneratedImageService(repo, root)
	defer s.Stop()
	b64 := tinyArchivePNG(t)
	payload := []byte(`{"data":[{"b64_json":"` + b64 + `"}]}`)
	owner := GeneratedImageOwner{UserID: 12, RequestID: "req-1", Model: "gpt-image-2"}
	if !s.Submit(owner, payload) {
		t.Fatal("submit rejected")
	}
	payload[0] = 'X'
	if !s.Submit(owner, []byte(`{"data":[{"b64_json":"`+b64+`"}]}`)) {
		t.Fatal("second submit rejected")
	}
	deadline := time.Now().Add(3 * time.Second)
	var item GeneratedImage
	for time.Now().Before(deadline) {
		repo.mu.Lock()
		for _, v := range repo.images {
			item = v
		}
		repo.mu.Unlock()
		if item.ID != "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if item.ID == "" {
		t.Fatal("worker did not persist")
	}
	time.Sleep(80 * time.Millisecond)
	repo.mu.Lock()
	count := len(repo.images)
	repo.mu.Unlock()
	if count != 1 {
		t.Fatalf("duplicate rows: %d", count)
	}
	f, _, err := s.Content(context.Background(), item.ID)
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	if _, _, err := s.Content(context.Background(), "../etc/passwd"); err == nil {
		t.Fatal("path traversal accepted")
	}
	if err := s.Delete(context.Background(), item.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, item.ID+".png")); !os.IsNotExist(err) {
		t.Fatalf("file remains: %v", err)
	}
}
func TestGeneratedImageLimitsAndOrphans(t *testing.T) {
	root := t.TempDir()
	repo := &archiveMemoryRepo{images: map[string]GeneratedImage{}, keys: map[string]bool{}}
	s := NewGeneratedImageService(repo, root)
	defer s.Stop()
	if s.Submit(GeneratedImageOwner{UserID: 1}, []byte(strings.Repeat("x", (64<<20)+1))) {
		t.Fatal("oversized accepted")
	}
	id := strings.Repeat("a", 32)
	p := filepath.Join(root, id+".png")
	if err := os.WriteFile(p, []byte("orphan"), 0600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(p, old, old); err != nil {
		t.Fatal(err)
	}
	if err := s.Cleanup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatalf("orphan remains: %v", err)
	}
}
