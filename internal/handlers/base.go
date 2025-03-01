package handlers

import (
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/sdko-org/registry-proxy/internal/config"
	"github.com/sdko-org/registry-proxy/internal/dockerhub"
	"github.com/sdko-org/registry-proxy/internal/storage"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
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
	db          *gorm.DB
}

func NewProxyHandler(logger *logrus.Logger, cfg *config.Config, storage storage.Storage, dhClient *dockerhub.Client, db *gorm.DB) *ProxyHandler {
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
		db:       db,
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

	if len(parts) >= 3 && parts[len(parts)-2] == "tags" && parts[len(parts)-1] == "list" {
		image := strings.Join(parts[:len(parts)-2], "/")
		h.handleTagsList(w, r, image)
		return
	}

	if path == "_catalog" {
		HandleCatalog(w, r)
		return
	}

	for _, part := range parts {
		if strings.Contains(part, "..") || strings.Contains(part, "//") {
			http.Error(w, "Invalid path component", http.StatusBadRequest)
			return
		}
	}

	if len(parts) < 2 {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	resourceType := parts[len(parts)-2]
	reference := parts[len(parts)-1]
	image := strings.Join(parts[:len(parts)-2], "/")

	switch resourceType {
	case "manifests":
		h.handleManifest(w, r, image, reference)
	case "blobs":
		h.handleBlob(w, r, image, reference)
	default:
		http.Error(w, "Not found", http.StatusNotFound)
	}
}

func normalizeImageName(image string) string {
	if !strings.Contains(image, "/") {
		return "library/" + image
	}
	return image
}

func safeFilename(digest string) string {
	safe := safeFilenameChars.ReplaceAllString(digest, "_")
	if len(safe) > 255 {
		return safe[:255]
	}
	return safe
}
