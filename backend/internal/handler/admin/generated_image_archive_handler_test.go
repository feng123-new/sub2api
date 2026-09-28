package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

type generatedImageHandlerRepo struct{ filter service.GeneratedImageFilter }

func (r *generatedImageHandlerRepo) Insert(context.Context, service.GeneratedImage) (bool, error) {
	return false, nil
}
func (r *generatedImageHandlerRepo) List(_ context.Context, f service.GeneratedImageFilter) (service.GeneratedImageList, error) {
	r.filter = f
	return service.GeneratedImageList{}, nil
}
func (r *generatedImageHandlerRepo) Get(context.Context, string) (service.GeneratedImage, error) {
	return service.GeneratedImage{}, service.ErrGeneratedImageNotFound
}
func (r *generatedImageHandlerRepo) Delete(context.Context, string) (bool, error) { return false, nil }
func (r *generatedImageHandlerRepo) Prune(context.Context, time.Time, int64) ([]string, error) {
	return nil, nil
}
func (r *generatedImageHandlerRepo) HasID(context.Context, string) (bool, error) { return false, nil }

func TestGeneratedImageHandlerFilters(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &generatedImageHandlerRepo{}
	svc := service.NewGeneratedImageService(repo, t.TempDir())
	defer svc.Stop()
	h := NewGeneratedImageHandler(svc)
	r := gin.New()
	r.GET("/generated-images", h.List)
	for _, tc := range []struct {
		url    string
		status int
	}{
		{"/generated-images?page=2&user_id=13&model=im&start_date=2026-09-01&end_date=2026-09-03", 200},
		{"/generated-images?user_id=0", 400},
		{"/generated-images?start_date=invalid", 400},
	} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, tc.url, nil))
		if w.Code != tc.status {
			t.Fatalf("%s status %d: %s", tc.url, w.Code, w.Body.String())
		}
		if tc.status == 200 {
			if !strings.Contains(w.Body.String(), `"retention_days":30`) || !strings.Contains(w.Body.String(), `"page_size":24`) {
				t.Fatalf("missing response fields: %s", w.Body.String())
			}
			if repo.filter.UserID != 13 || repo.filter.Page != 2 || repo.filter.StartDate == nil || repo.filter.EndDate == nil || repo.filter.EndDate.Sub(*repo.filter.StartDate) != 72*time.Hour {
				t.Fatalf("wrong filter: %+v", repo.filter)
			}
		}
	}
}
func TestGeneratedImageHandlerContentTraversalHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := NewGeneratedImageHandler(service.NewGeneratedImageService(nil, t.TempDir()))
	r := gin.New()
	r.GET("/generated-images/:id/content", h.Content)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/generated-images/not-an-id/content", nil))
	if w.Code != 404 || w.Header().Get("Cache-Control") != "private, no-store" || w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("unexpected response: %d %v", w.Code, w.Header())
	}
}
