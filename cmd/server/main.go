package main

import (
	"context"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gorilla/mux"
	"github.com/sdko-org/registry-proxy/internal/config"
	"github.com/sdko-org/registry-proxy/internal/database"
	"github.com/sdko-org/registry-proxy/internal/dockerhub"
	"github.com/sdko-org/registry-proxy/internal/handlers"
	"github.com/sdko-org/registry-proxy/internal/models"
	"github.com/sdko-org/registry-proxy/internal/storage"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

var logger = logrus.New()

func main() {
	configureLogger()
	logger.Info("Starting registry proxy server")

	cfg, err := config.Load(logger)
	if err != nil {
		logger.WithError(err).Fatal("Failed to load configuration")
	}

	db := initializeDatabase(cfg)
	s3Storage := storage.NewS3Storage(logger, cfg, db)
	dhClient := dockerhub.NewClient(logger, cfg)

	router := setupRouter(cfg, db, s3Storage, dhClient)
	server := configureServer(router)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go startCachePurger(ctx, logger, db, s3Storage, cfg)
	go handleGracefulShutdown(server)

	logger.Info("Server listening on :8080")
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.WithError(err).Fatal("Server failed to start")
	}
}

func configureLogger() {
	logger.SetFormatter(&logrus.JSONFormatter{
		TimestampFormat: time.RFC3339Nano,
	})
	logger.SetOutput(os.Stdout)
	if os.Getenv("DEBUG") == "true" {
		logger.SetLevel(logrus.DebugLevel)
	} else {
		logger.SetLevel(logrus.InfoLevel)
	}
}

func initializeDatabase(cfg *config.Config) *gorm.DB {
	db, err := database.NewPostgresDB(logger, database.PostgresConfig{
		User:     cfg.PostgresUser,
		Password: cfg.PostgresPassword,
		Host:     cfg.PostgresHost,
		Port:     cfg.PostgresPort,
		DBName:   cfg.PostgresDatabase,
		SSLMode:  cfg.PostgresSSLMode,
	})
	if err != nil {
		logger.WithError(err).Fatal("Database initialization failed")
	}
	return db
}

func setupRouter(cfg *config.Config, db *gorm.DB, storage storage.Storage, dhClient *dockerhub.Client) *mux.Router {
	r := mux.NewRouter()
	r.Use(handlers.LoggingMiddleware(logger, db))
	r.Use(handlers.RateLimitMiddleware(cfg))

	r.HandleFunc("/v2/", handlers.HandleV2Check).Methods("GET")
	r.PathPrefix("/v2/").Handler(handlers.NewProxyHandler(logger, cfg, storage, dhClient))
	return r
}

func configureServer(handler http.Handler) *http.Server {
	return &http.Server{
		Addr:         ":8080",
		Handler:      handler,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  120 * time.Second,
	}
}

func handleGracefulShutdown(server *http.Server) {
	sigint := make(chan os.Signal, 1)
	signal.Notify(sigint, syscall.SIGINT, syscall.SIGTERM)
	<-sigint

	logger.Info("Initiating graceful shutdown")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil {
		logger.WithError(err).Error("Server shutdown error")
	}
}

func startCachePurger(ctx context.Context, log *logrus.Logger, db *gorm.DB, storage storage.Storage, cfg *config.Config) {
	ticker := time.NewTicker(30 * time.Minute)
	defer ticker.Stop()

	logEntry := log.WithField("component", "cache_purger")
	logEntry.Info("Starting cache purger")

	for {
		select {
		case <-ticker.C:
			purgeExpiredCache(ctx, logEntry, db, storage)
		case <-ctx.Done():
			logEntry.Info("Stopping cache purger")
			return
		}
	}
}

func purgeExpiredCache(ctx context.Context, log *logrus.Entry, db *gorm.DB, storage storage.Storage) {
	start := time.Now()
	log = log.WithField("operation", "cache_purge")

	var entries []models.CacheEntry
	if err := db.WithContext(ctx).
		Where("expires_at < ? OR last_access < ?",
			time.Now(),
			time.Now().Add(-7*24*time.Hour)).
		Find(&entries).Error; err != nil {
		log.WithError(err).Error("Cache purge query failed")
		return
	}

	log.WithField("count", len(entries)).Info("Processing expired cache entries")
	deleted := 0

	for _, entry := range entries {
		if err := storage.Delete(ctx, entry.Key); err != nil {
			log.WithFields(logrus.Fields{
				"key":   entry.Key,
				"error": err,
			}).Error("Failed to delete cache entry")
			continue
		}
		deleted++
	}

	log.WithFields(logrus.Fields{
		"deleted_entries": deleted,
		"failed_deletes":  len(entries) - deleted,
		"duration":        time.Since(start),
	}).Info("Cache purge completed")
}
