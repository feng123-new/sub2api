package service

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "golang.org/x/image/webp"
)

const (
	GeneratedImageRetentionDays         = 30
	generatedImageMaxPayload            = 64 << 20
	generatedImageMaxBytes              = 20 << 20
	generatedImageMaxArchiveBytes int64 = 10 << 30
)

var ErrGeneratedImageNotFound = errors.New("generated image not found")

type GeneratedImageOwner struct {
	UserID, APIKeyID, GroupID, AccountID int64
	Model, RequestID, Endpoint           string
}

type GeneratedImage struct {
	ID        string    `json:"id"`
	UserID    int64     `json:"user_id"`
	UserEmail string    `json:"user_email"`
	GroupID   int64     `json:"group_id"`
	GroupName string    `json:"group_name"`
	APIKeyID  int64     `json:"api_key_id"`
	AccountID int64     `json:"account_id"`
	Model     string    `json:"model"`
	RequestID string    `json:"request_id"`
	Endpoint  string    `json:"endpoint"`
	MIMEType  string    `json:"mime_type"`
	ByteSize  int64     `json:"byte_size"`
	Width     int       `json:"width"`
	Height    int       `json:"height"`
	SHA256    string    `json:"-"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

type GeneratedImageFilter struct {
	Page, PageSize     int
	UserID             int64
	Model              string
	StartDate, EndDate *time.Time
}

type GeneratedImageList struct {
	Items         []GeneratedImage `json:"items"`
	Total         int64            `json:"total"`
	Page          int              `json:"page"`
	PageSize      int              `json:"page_size"`
	RetentionDays int              `json:"retention_days"`
}

type GeneratedImageRepository interface {
	Insert(context.Context, GeneratedImage) (bool, error)
	List(context.Context, GeneratedImageFilter) (GeneratedImageList, error)
	Get(context.Context, string) (GeneratedImage, error)
	Delete(context.Context, string) (bool, error)
	Prune(context.Context, time.Time, int64) ([]string, error)
	HasID(context.Context, string) (bool, error)
}

type generatedImageJob struct {
	owner   GeneratedImageOwner
	payload []byte
}
type generatedImageSource struct{ b64, url string }
type GeneratedImageService struct {
	repo   GeneratedImageRepository
	root   string
	jobs   chan generatedImageJob
	slots  chan struct{}
	done   chan struct{}
	ctx    context.Context
	cancel context.CancelFunc
	once   sync.Once
	wg     sync.WaitGroup
	client *http.Client
}

func NewGeneratedImageService(repo GeneratedImageRepository, root string) *GeneratedImageService {
	if root == "" {
		dir := os.Getenv("DATA_DIR")
		if dir == "" {
			dir = "/app/data"
		}
		root = filepath.Join(dir, "generated-images")
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &GeneratedImageService{repo: repo, root: root, jobs: make(chan generatedImageJob, 2), slots: make(chan struct{}, 3), done: make(chan struct{}), ctx: ctx, cancel: cancel, client: newGeneratedImageHTTPClient()}
	if repo != nil {
		s.wg.Add(1)
		go s.run()
	}
	return s
}

// Submit never waits on IO or the archive worker. A queued job owns its payload copy.
func (s *GeneratedImageService) Submit(owner GeneratedImageOwner, payload []byte) bool {
	if s == nil || s.repo == nil || owner.UserID <= 0 || len(payload) == 0 || len(payload) > generatedImageMaxPayload || len(s.jobs) == cap(s.jobs) {
		return false
	}
	if !bytes.Contains(payload, []byte(`"b64_json"`)) && !bytes.Contains(payload, []byte(`"url"`)) && !bytes.Contains(payload, []byte(`"result"`)) && !bytes.Contains(payload, []byte(`"inlineData"`)) && !bytes.Contains(payload, []byte(`"inline_data"`)) {
		return false
	}
	select {
	case <-s.done:
		return false
	default:
	}
	select {
	case s.slots <- struct{}{}:
	default:
		return false
	}
	job := generatedImageJob{owner: owner, payload: append([]byte(nil), payload...)}
	select {
	case s.jobs <- job:
		return true
	case <-s.done:
		<-s.slots
		return false
	default:
		<-s.slots
		return false
	}
}

func (s *GeneratedImageService) Stop() {
	if s == nil {
		return
	}
	s.once.Do(func() {
		close(s.done)
		if s.cancel != nil {
			s.cancel()
		}
	})
	s.wg.Wait()
}
func (s *GeneratedImageService) run() {
	defer s.wg.Done()
	ctx, cancel := context.WithTimeout(s.ctx, 30*time.Second)
	if err := s.Cleanup(ctx); err != nil {
		slog.Warn("generated_image_cleanup_failed", "error", err)
	}
	cancel()
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case job := <-s.jobs:
			s.archive(job)
			<-s.slots
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(s.ctx, 30*time.Second)
			if err := s.Cleanup(ctx); err != nil {
				slog.Warn("generated_image_cleanup_failed", "error", err)
			}
			cancel()
		case <-s.done:
			return
		}
	}
}

func (s *GeneratedImageService) archive(job generatedImageJob) {
	for _, source := range parseGeneratedImages(job.payload) {
		if s.ctx.Err() != nil {
			return
		}
		var data []byte
		if source.b64 != "" {
			b64 := source.b64
			if i := strings.Index(b64, ","); strings.HasPrefix(b64, "data:image/") && i >= 0 {
				b64 = b64[i+1:]
			}
			if len(b64) > (generatedImageMaxBytes+2)/3*4+8 {
				continue
			}
			var err error
			data, err = base64.StdEncoding.DecodeString(b64)
			if err != nil {
				data, err = base64.RawStdEncoding.DecodeString(b64)
			}
			if err != nil {
				continue
			}
		}
		if source.b64 == "" && source.url != "" {
			ctx, cancel := context.WithTimeout(s.ctx, 12*time.Second)
			var err error
			data, err = s.download(ctx, source.url)
			cancel()
			if err != nil {
				slog.Warn("generated_image_download_failed", "request_id", job.owner.RequestID, "error", err)
				continue
			}
		}
		if len(data) == 0 || len(data) > generatedImageMaxBytes {
			continue
		}
		mime := http.DetectContentType(data)
		ext := ""
		switch mime {
		case "image/png":
			ext = ".png"
		case "image/jpeg":
			ext = ".jpg"
		case "image/webp":
			ext = ".webp"
		case "image/gif":
			ext = ".gif"
		default:
			continue
		}
		config, format, err := image.DecodeConfig(bytes.NewReader(data))
		if err != nil || config.Width <= 0 || config.Height <= 0 || (format == "jpeg" && mime != "image/jpeg") || (format != "jpeg" && "image/"+format != mime) {
			continue
		}
		if err = os.MkdirAll(s.root, 0700); err != nil {
			slog.Warn("generated_image_directory_failed", "error", err)
			continue
		}
		random := make([]byte, 16)
		if _, err = rand.Read(random); err != nil {
			continue
		}
		id := hex.EncodeToString(random)
		path := filepath.Join(s.root, id+ext)
		f, e := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if e != nil {
			slog.Warn("generated_image_write_failed", "error", e)
			continue
		}
		_, err = f.Write(data)
		closeErr := f.Close()
		if err != nil || closeErr != nil {
			_ = os.Remove(path)
			continue
		}
		digest := sha256.Sum256(data)
		now := time.Now().UTC()
		item := GeneratedImage{ID: id, UserID: job.owner.UserID, APIKeyID: job.owner.APIKeyID, GroupID: job.owner.GroupID, AccountID: job.owner.AccountID, Model: truncateGeneratedImageField(job.owner.Model, 200), RequestID: truncateGeneratedImageField(job.owner.RequestID, 200), Endpoint: truncateGeneratedImageField(job.owner.Endpoint, 200), MIMEType: mime, ByteSize: int64(len(data)), Width: config.Width, Height: config.Height, SHA256: hex.EncodeToString(digest[:]), CreatedAt: now, ExpiresAt: now.Add(GeneratedImageRetentionDays * 24 * time.Hour)}
		ctx, cancel := context.WithTimeout(s.ctx, 5*time.Second)
		inserted, e := s.repo.Insert(ctx, item)
		cancel()
		if e != nil || !inserted {
			_ = os.Remove(path)
			if e != nil {
				slog.Warn("generated_image_index_failed", "request_id", job.owner.RequestID, "error", e)
			}
		}
	}
}
func truncateGeneratedImageField(v string, limit int) string {
	r := []rune(v)
	if len(r) > limit {
		return string(r[:limit])
	}
	return v
}

func (s *GeneratedImageService) List(ctx context.Context, f GeneratedImageFilter) (GeneratedImageList, error) {
	if f.Page < 1 {
		f.Page = 1
	}
	if f.PageSize < 1 {
		f.PageSize = 24
	}
	if f.PageSize > 100 {
		f.PageSize = 100
	}
	v, e := s.repo.List(ctx, f)
	v.Page = f.Page
	v.PageSize = f.PageSize
	v.RetentionDays = GeneratedImageRetentionDays
	if v.Items == nil {
		v.Items = []GeneratedImage{}
	}
	return v, e
}
func generatedImageValidID(id string) bool {
	if len(id) != 32 {
		return false
	}
	for _, r := range id {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return false
		}
	}
	return true
}
func generatedImageFilePath(root string, item GeneratedImage) string {
	switch item.MIMEType {
	case "image/png":
		return filepath.Join(root, item.ID+".png")
	case "image/jpeg":
		return filepath.Join(root, item.ID+".jpg")
	case "image/webp":
		return filepath.Join(root, item.ID+".webp")
	case "image/gif":
		return filepath.Join(root, item.ID+".gif")
	}
	return ""
}
func (s *GeneratedImageService) Content(ctx context.Context, id string) (*os.File, GeneratedImage, error) {
	if !generatedImageValidID(id) {
		return nil, GeneratedImage{}, ErrGeneratedImageNotFound
	}
	v, e := s.repo.Get(ctx, id)
	if e != nil {
		return nil, v, e
	}
	if !v.ExpiresAt.After(time.Now()) {
		return nil, v, ErrGeneratedImageNotFound
	}
	path := generatedImageFilePath(s.root, v)
	if path == "" {
		return nil, v, ErrGeneratedImageNotFound
	}
	info, e := os.Lstat(path)
	if e != nil || !info.Mode().IsRegular() || info.Size() != v.ByteSize {
		return nil, v, ErrGeneratedImageNotFound
	}
	f, e := os.Open(path)
	if e != nil {
		return nil, v, ErrGeneratedImageNotFound
	}
	return f, v, nil
}
func (s *GeneratedImageService) Delete(ctx context.Context, id string) error {
	if !generatedImageValidID(id) {
		return ErrGeneratedImageNotFound
	}
	v, e := s.repo.Get(ctx, id)
	if e != nil {
		return e
	}
	removed, e := s.repo.Delete(ctx, id)
	if e != nil {
		return e
	}
	if !removed {
		return ErrGeneratedImageNotFound
	}
	path := generatedImageFilePath(s.root, v)
	if path != "" {
		if e = os.Remove(path); e != nil && !os.IsNotExist(e) {
			return e
		}
	}
	return nil
}
func (s *GeneratedImageService) Cleanup(ctx context.Context) error {
	if s == nil || s.repo == nil {
		return nil
	}
	ids, err := s.repo.Prune(ctx, time.Now().UTC(), generatedImageMaxArchiveBytes)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if generatedImageValidID(id) {
			for _, ext := range []string{".png", ".jpg", ".webp", ".gif"} {
				_ = os.Remove(filepath.Join(s.root, id+ext))
			}
		}
	}
	entries, err := os.ReadDir(s.root)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		ext := filepath.Ext(name)
		id := strings.TrimSuffix(name, ext)
		if !generatedImageValidID(id) || (ext != ".png" && ext != ".jpg" && ext != ".webp" && ext != ".gif") {
			continue
		}
		info, e := entry.Info()
		if e != nil || time.Since(info.ModTime()) < time.Hour {
			continue
		}
		exists, e := s.repo.HasID(ctx, id)
		if e != nil {
			return e
		}
		if !exists {
			_ = os.Remove(filepath.Join(s.root, name))
		}
	}
	return nil
}

var generatedImageDeniedNetworks = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("2001:db8::/32"), netip.MustParsePrefix("64:ff9b::/96"),
}

func validGeneratedImageIP(ip net.IP) bool {
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	addr = addr.Unmap()
	for _, network := range generatedImageDeniedNetworks {
		if network.Contains(addr) {
			return false
		}
	}
	return ip.IsGlobalUnicast() && !ip.IsPrivate() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() && !ip.IsLinkLocalMulticast() && !ip.IsMulticast() && !ip.IsUnspecified()
}
func validGeneratedImageURL(raw string) (*url.URL, error) {
	u, e := url.Parse(raw)
	if e != nil || u == nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return nil, errors.New("invalid image URL")
	}
	return u, nil
}
func newGeneratedImageHTTPClient() *http.Client {
	dialer := &net.Dialer{Timeout: 3 * time.Second, KeepAlive: -1}
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true, MaxIdleConns: 0, TLSHandshakeTimeout: 4 * time.Second, ResponseHeaderTimeout: 5 * time.Second}
	transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, e := net.SplitHostPort(addr)
		if e != nil {
			return nil, e
		}
		ips, e := net.DefaultResolver.LookupIPAddr(ctx, host)
		if e != nil || len(ips) == 0 {
			return nil, errors.New("image host could not be resolved")
		}
		for _, ip := range ips {
			if !validGeneratedImageIP(ip.IP) {
				return nil, errors.New("image host resolves to non-public address")
			}
		}
		return dialer.DialContext(ctx, network, net.JoinHostPort(ips[0].IP.String(), port))
	}
	return &http.Client{Timeout: 12 * time.Second, Transport: transport, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 3 {
			return errors.New("too many image redirects")
		}
		_, e := validGeneratedImageURL(req.URL.String())
		return e
	}}
}
func (s *GeneratedImageService) download(ctx context.Context, raw string) ([]byte, error) {
	if _, err := validGeneratedImageURL(raw); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, errors.New("image fetch failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("image fetch status %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, generatedImageMaxBytes+1))
	if err != nil || len(data) > generatedImageMaxBytes {
		return nil, errors.New("image exceeds limit")
	}
	return data, nil
}

func parseGeneratedImages(payload []byte) []generatedImageSource {
	if len(payload) == 0 || len(payload) > generatedImageMaxPayload {
		return nil
	}
	var out []generatedImageSource
	add := func(m map[string]json.RawMessage) {
		if len(out) >= 8 {
			return
		}
		var src generatedImageSource
		_ = json.Unmarshal(m["b64_json"], &src.b64)
		_ = json.Unmarshal(m["url"], &src.url)
		if src.b64 == "" && strings.HasPrefix(src.url, "data:image/") {
			src.b64, src.url = src.url, ""
		}
		if src.b64 != "" || src.url != "" {
			out = append(out, src)
		}
	}
	var walk func([]byte)
	walk = func(raw []byte) {
		var obj map[string]json.RawMessage
		if json.Unmarshal(raw, &obj) != nil {
			return
		}
		var typ, status string
		_ = json.Unmarshal(obj["type"], &typ)
		_ = json.Unmarshal(obj["status"], &status)
		switch typ {
		case "image_generation.partial_image", "image_edit.partial_image", "response.output_item.added", "response.output_item.delta":
			return
		case "image_generation.completed", "image_edit.completed":
			add(obj)
			readGeneratedImageData(obj["data"], add)
			if len(obj["result"]) > 0 {
				readGeneratedImageResult(obj["result"], add)
			}
			return
		case "response.output_item.done":
			walk(obj["item"])
			return
		case "response.completed", "response.done":
			walk(obj["response"])
			return
		case "image_generation_call":
			if status != "" && status != "completed" {
				return
			}
			readGeneratedImageResult(obj["result"], add)
			return
		}
		if typ != "" && typ != "response" {
			return
		}
		if wrapped, ok := obj["response"]; ok {
			walk(wrapped)
		}
		var candidates []struct {
			Content struct {
				Parts []map[string]json.RawMessage `json:"parts"`
			} `json:"content"`
		}
		if json.Unmarshal(obj["candidates"], &candidates) == nil {
			for _, candidate := range candidates {
				for _, part := range candidate.Content.Parts {
					for _, key := range []string{"inlineData", "inline_data"} {
						var inline map[string]json.RawMessage
						if json.Unmarshal(part[key], &inline) == nil {
							var mime string
							_ = json.Unmarshal(inline["mimeType"], &mime)
							if mime == "" {
								_ = json.Unmarshal(inline["mime_type"], &mime)
							}
							if strings.HasPrefix(mime, "image/") {
								add(map[string]json.RawMessage{"b64_json": inline["data"]})
							}
						}
					}
				}
			}
		}
		readGeneratedImageData(obj["data"], add)
		var items []json.RawMessage
		if json.Unmarshal(obj["output"], &items) == nil {
			for _, item := range items {
				walk(item)
			}
		}
	}
	if json.Valid(payload) {
		walk(payload)
		return out
	}
	// Streaming submissions may contain several SSE data frames; only terminal event JSON is inspected.
	for _, line := range bytes.Split(payload, []byte{'\n'}) {
		line = bytes.TrimSpace(line)
		if bytes.HasPrefix(line, []byte("data:")) {
			walk(bytes.TrimSpace(line[5:]))
		}
	}
	return out
}
func readGeneratedImageData(raw json.RawMessage, add func(map[string]json.RawMessage)) {
	var items []map[string]json.RawMessage
	if json.Unmarshal(raw, &items) == nil {
		for _, item := range items {
			add(item)
		}
	}
}
func readGeneratedImageResult(raw json.RawMessage, add func(map[string]json.RawMessage)) {
	var b64 string
	if json.Unmarshal(raw, &b64) == nil {
		add(map[string]json.RawMessage{"b64_json": raw})
		return
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) == nil {
		add(obj)
		readGeneratedImageData(obj["data"], add)
	}
}
