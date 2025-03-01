package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sdko-org/registry-proxy/internal/config"
	"github.com/sdko-org/registry-proxy/internal/dockerhub"
	"github.com/sdko-org/registry-proxy/internal/storage"
	"github.com/sirupsen/logrus"
)

type ProxyHandler struct {
	cfg         *config.Config
	storage     storage.Storage
	dhClient    *dockerhub.Client
	log         *logrus.Entry
	downloadMap sync.Map
	tempDir     string
}

func NewProxyHandler(logger *logrus.Logger, cfg *config.Config, storage storage.Storage, dhClient *dockerhub.Client) *ProxyHandler {
	return &ProxyHandler{
		cfg:      cfg,
		storage:  storage,
		dhClient: dhClient,
		log:      logger.WithField("component", "proxy_handler"),
		tempDir:  cfg.TempDir,
	}
}

func (h *ProxyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/v2/")
	parts := strings.Split(path, "/")

	if len(parts) < 2 {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	image := strings.Join(parts[:len(parts)-2], "/")
	resourceType := parts[len(parts)-2]
	reference := parts[len(parts)-1]

	switch resourceType {
	case "manifests":
		h.handleManifest(w, r, image, reference)
	case "blobs":
		h.handleBlob(w, r, image, reference)
	default:
		http.Error(w, "Not found", http.StatusNotFound)
	}
}

func (h *ProxyHandler) handleManifest(w http.ResponseWriter, r *http.Request, image, reference string) {
	ctx := r.Context()
	cacheKey := fmt.Sprintf("manifests/%s/%s", image, reference)

	content, digest, mediaType, err := h.storage.Get(ctx, cacheKey)
	if err == nil {
		w.Header().Set("Content-Type", mediaType)
		w.Header().Set("Docker-Content-Digest", digest)
		w.Header().Set("Content-Length", fmt.Sprint(len(content)))
		w.WriteHeader(http.StatusOK)
		w.Write(content)
		return
	}

	resp, err := h.dhClient.GetManifest(ctx, image, reference, r.Header.Get("Accept"))
	if err != nil {
		http.Error(w, "Failed to fetch manifest", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		forwardResponse(w, resp)
		return
	}

	body, _ := io.ReadAll(resp.Body)
	mediaType = resp.Header.Get("Content-Type")
	digest = resp.Header.Get("Docker-Content-Digest")
	if digest == "" {
		hash := sha256.Sum256(body)
		digest = "sha256:" + hex.EncodeToString(hash[:])
	}

	if err := h.storage.Put(ctx, cacheKey, body, digest, mediaType, h.cfg.CacheTTL); err != nil {
		h.log.WithError(err).Error("Failed to cache manifest")
	}

	w.Header().Set("Content-Type", mediaType)
	w.Header().Set("Docker-Content-Digest", digest)
	w.WriteHeader(resp.StatusCode)
	w.Write(body)
}

func (h *ProxyHandler) handleBlob(w http.ResponseWriter, r *http.Request, image, digest string) {
	ctx := r.Context()
	tempPath := filepath.Join(h.tempDir, sanitizeFilename(digest))

	if h.serveFromTempFile(w, tempPath, digest) {
		h.log.Debug("Served from local temp file")
		return
	}

	if waitChan, exists := h.downloadMap.Load(digest); exists {
		h.log.Debug("Waiting for existing download")
		<-waitChan.(chan struct{})
		if h.serveFromTempFile(w, tempPath, digest) {
			return
		}
	}

	h.downloadMap.Store(digest, make(chan struct{}))
	defer h.downloadMap.Delete(digest)

	resp, err := h.dhClient.GetBlob(ctx, image, digest)
	if err != nil {
		http.Error(w, "Blob fetch failed", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	tempFile, err := os.Create(tempPath)
	if err != nil {
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	defer tempFile.Close()

	hash := sha256.New()
	multiWriter := io.MultiWriter(tempFile, hash, w)

	w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
	w.Header().Set("Docker-Content-Digest", digest)

	_, copyErr := io.Copy(multiWriter, resp.Body)
	if copyErr != nil {
		os.Remove(tempPath)
		http.Error(w, "Download failed", http.StatusInternalServerError)
		return
	}

	calculatedDigest := "sha256:" + hex.EncodeToString(hash.Sum(nil))
	if calculatedDigest != digest {
		os.Remove(tempPath)
		http.Error(w, "Digest mismatch", http.StatusBadGateway)
		return
	}

	go h.backgroundUploadToS3(tempPath, digest, image)
}

func (h *ProxyHandler) serveFromTempFile(w http.ResponseWriter, path, digest string) bool {
	if _, err := os.Stat(path); err == nil {
		f, err := os.Open(path)
		if err != nil {
			return false
		}
		defer f.Close()

		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Docker-Content-Digest", digest)
		io.Copy(w, f)
		return true
	}
	return false
}

func (h *ProxyHandler) backgroundUploadToS3(tempPath, digest, image string) {
	defer os.Remove(tempPath)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	f, err := os.Open(tempPath)
	if err != nil {
		h.log.WithError(err).Error("Failed to open temp file for S3 upload")
		return
	}
	defer f.Close()

	cacheKey := fmt.Sprintf("blobs/%s/%s", image, digest)
	mediaType := "application/octet-stream"

	for attempt := 1; attempt <= 5; attempt++ {
		if err := h.storage.PutStream(ctx, cacheKey, f, digest, mediaType, h.cfg.CacheTTL); err == nil {
			h.log.WithField("digest", digest).Info("S3 upload successful")
			return
		}

		h.log.WithFields(logrus.Fields{
			"attempt": attempt,
			"digest":  digest,
		}).Warn("S3 upload failed, retrying")

		f.Seek(0, 0)
		time.Sleep(time.Duration(attempt*2) * time.Second)
	}

	h.log.WithField("digest", digest).Error("Failed S3 upload after retries")
}

func forwardResponse(w http.ResponseWriter, resp *http.Response) {
	for k, v := range resp.Header {
		w.Header()[k] = v
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

func sanitizeFilename(digest string) string {
	return strings.ReplaceAll(digest, ":", "_")
}
