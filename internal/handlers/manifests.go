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
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/sdko-org/registry-proxy/internal/config"
	"github.com/sdko-org/registry-proxy/internal/dockerhub"
	"github.com/sdko-org/registry-proxy/internal/storage"
	"github.com/sirupsen/logrus"
)

var (
	validDigestRegex  = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
	safeFilenameChars = regexp.MustCompile(`[^a-zA-Z0-9-_]`)
	pathValidator     = regexp.MustCompile(`^[a-zA-Z0-9-_:\\./]+$`)
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
	if err := os.MkdirAll(cfg.TempDir, 0700); err != nil {
		logger.Fatal(err)
	}
	if err := os.Chmod(cfg.TempDir, 0700); err != nil {
		logger.Fatal(err)
	}
	testFile := filepath.Join(cfg.TempDir, ".testwrite")
	if err := os.WriteFile(testFile, []byte("test"), 0600); err != nil {
		logger.Fatal(err)
	}
	os.Remove(testFile)
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
	if !pathValidator.MatchString(path) {
		http.Error(w, "Invalid path", http.StatusBadRequest)
		return
	}
	parts := strings.Split(path, "/")
	if len(parts) < 2 {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}
	for _, part := range parts {
		if strings.Contains(part, "..") || strings.Contains(part, "//") {
			http.Error(w, "Invalid path component", http.StatusBadRequest)
			return
		}
	}
	image := strings.Join(parts[:len(parts)-2], "/")
	resourceType := parts[len(parts)-2]
	reference := parts[len(parts)-1]
	if !validDigestRegex.MatchString(reference) && !pathValidator.MatchString(reference) {
		http.Error(w, "Invalid reference", http.StatusBadRequest)
		return
	}
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
	ctx := context.Background()
	cacheKey := fmt.Sprintf("manifests/%s/%s", image, reference)
	content, digest, mediaType, err := h.storage.Get(ctx, cacheKey)
	if err == nil {
		h.log.WithFields(logrus.Fields{
			"image":     image,
			"reference": reference,
			"source":    "s3",
		}).Info("Serving manifest from cache")
		w.Header().Set("Content-Type", mediaType)
		w.Header().Set("Docker-Content-Digest", digest)
		w.Header().Set("Content-Length", fmt.Sprint(len(content)))
		w.WriteHeader(http.StatusOK)
		w.Write(content)
		return
	}

	h.log.WithFields(logrus.Fields{
		"image":     image,
		"reference": reference,
		"source":    "dockerhub",
	}).Info("Fetching manifest from upstream")
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

func (h *ProxyHandler) handleBlob(w http.ResponseWriter, image, digest string) {
	if !validDigestRegex.MatchString(digest) {
		http.Error(w, "Invalid digest format", http.StatusBadRequest)
		return
	}
	ctx := context.Background()

	cacheKey := fmt.Sprintf("blobs/%s/%s", image, digest)
	content, retrievedDigest, mediaType, err := h.storage.Get(ctx, cacheKey)
	if err == nil {
		h.log.WithFields(logrus.Fields{
			"digest": digest,
			"source": "s3",
		}).Info("Serving blob from persistent cache")
		w.Header().Set("Content-Type", mediaType)
		w.Header().Set("Docker-Content-Digest", retrievedDigest)
		w.Header().Set("Content-Length", fmt.Sprint(len(content)))
		w.WriteHeader(http.StatusOK)
		w.Write(content)
		return
	}

	safeFilename := safeFilenameChars.ReplaceAllString(digest, "_")
	if len(safeFilename) > 255 {
		safeFilename = safeFilename[:255]
	}
	tempPath := filepath.Join(h.tempDir, safeFilename)
	if !strings.HasPrefix(tempPath, h.tempDir) {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}
	if h.serveFromTempFile(w, tempPath, digest) {
		return
	}
	if waitChan, exists := h.downloadMap.Load(digest); exists {
		<-waitChan.(chan struct{})
		if h.serveFromTempFile(w, tempPath, digest) {
			return
		}
	}
	h.downloadMap.Store(digest, make(chan struct{}))
	defer h.downloadMap.Delete(digest)

	h.log.WithFields(logrus.Fields{
		"digest": digest,
		"source": "dockerhub",
	}).Info("Downloading blob from upstream")
	resp, err := h.dhClient.GetBlob(ctx, image, digest)
	if err != nil {
		http.Error(w, "Blob fetch failed", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		forwardResponse(w, resp)
		return
	}
	tempFile, err := os.OpenFile(tempPath, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0600)
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
		h.log.WithFields(logrus.Fields{
			"expected": digest,
			"actual":   calculatedDigest,
			"source":   "dockerhub",
		}).Error("Blob digest mismatch")
		http.Error(w, "Digest mismatch", http.StatusBadGateway)
		return
	}
	go func() {
		defer os.Remove(tempPath)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		f, err := os.Open(tempPath)
		if err != nil {
			return
		}
		defer f.Close()
		cacheKey := fmt.Sprintf("blobs/%s/%s", image, digest)
		h.log.WithFields(logrus.Fields{
			"digest": digest,
			"source": "s3",
		}).Info("Storing blob in persistent cache")
		for attempt := 1; attempt <= 5; attempt++ {
			f.Seek(0, 0)
			if err := h.storage.PutStream(ctx, cacheKey, f, digest, "application/octet-stream", h.cfg.CacheTTL); err == nil {
				return
			}
			time.Sleep(time.Duration(attempt*2) * time.Second)
		}
	}()
}

func (h *ProxyHandler) serveFromTempFile(w http.ResponseWriter, path, digest string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || fi.Mode().Perm() != 0600 {
		return false
	}

	h.log.WithFields(logrus.Fields{
		"digest": digest,
		"source": "disk",
	}).Info("Serving blob from temporary storage")

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Docker-Content-Digest", digest)
	_, err = io.Copy(w, f)
	return err == nil
}

func forwardResponse(w http.ResponseWriter, resp *http.Response) {
	for k, v := range resp.Header {
		w.Header()[k] = v
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}
