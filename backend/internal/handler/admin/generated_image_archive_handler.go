package admin

import (
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

type GeneratedImageHandler struct {
	archive *service.GeneratedImageService
}

func NewGeneratedImageHandler(archive *service.GeneratedImageService) *GeneratedImageHandler {
	return &GeneratedImageHandler{archive: archive}
}

func generatedImageDate(raw string, end bool) (*time.Time, error) {
	if raw == "" {
		return nil, nil
	}
	date, err := time.Parse(time.DateOnly, raw)
	if err == nil {
		if end {
			date = date.AddDate(0, 0, 1)
		}
		return &date, nil
	}
	date, err = time.Parse(time.RFC3339, raw)
	if err != nil {
		return nil, err
	}
	return &date, nil
}
func (h *GeneratedImageHandler) List(c *gin.Context) {
	page, pageSize := response.ParsePagination(c)
	if c.Query("page_size") == "" {
		pageSize = 24
	}
	if pageSize > 100 {
		pageSize = 100
	}
	filter := service.GeneratedImageFilter{Page: page, PageSize: pageSize, Model: strings.TrimSpace(c.Query("model"))}
	if raw := strings.TrimSpace(c.Query("user_id")); raw != "" {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || id <= 0 {
			response.BadRequest(c, "Invalid user_id")
			return
		}
		filter.UserID = id
	}
	var err error
	filter.StartDate, err = generatedImageDate(strings.TrimSpace(c.Query("start_date")), false)
	if err != nil {
		response.BadRequest(c, "Invalid start_date")
		return
	}
	filter.EndDate, err = generatedImageDate(strings.TrimSpace(c.Query("end_date")), true)
	if err != nil {
		response.BadRequest(c, "Invalid end_date")
		return
	}
	items, err := h.archive.List(c.Request.Context(), filter)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, items)
}
func (h *GeneratedImageHandler) Content(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	c.Header("X-Content-Type-Options", "nosniff")
	f, item, err := h.archive.Content(c.Request.Context(), c.Param("id"))
	if err != nil {
		if errors.Is(err, service.ErrGeneratedImageNotFound) {
			response.Error(c, http.StatusNotFound, "Image not found")
		} else {
			response.ErrorFrom(c, err)
		}
		return
	}
	defer f.Close()
	c.Header("Content-Type", item.MIMEType)
	c.Header("Content-Length", strconv.FormatInt(item.ByteSize, 10))
	c.Status(http.StatusOK)
	_, _ = io.Copy(c.Writer, f)
}
func (h *GeneratedImageHandler) Delete(c *gin.Context) {
	err := h.archive.Delete(c.Request.Context(), c.Param("id"))
	if err != nil {
		if errors.Is(err, service.ErrGeneratedImageNotFound) {
			response.Error(c, http.StatusNotFound, "Image not found")
		} else {
			response.ErrorFrom(c, err)
		}
		return
	}
	response.Success(c, gin.H{"deleted": true})
}
