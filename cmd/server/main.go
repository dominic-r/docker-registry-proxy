package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gorilla/mux"
	"github.com/sdko-org/registry-proxy/internal/cache"
	"github.com/sdko-org/registry-proxy/internal/config"
	"github.com/sdko-org/registry-proxy/internal/database"
	"github.com/sdko-org/registry-proxy/internal/dockerhub"
	"github.com/sdko-org/registry-proxy/internal/handlers"
	httpserver "github.com/sdko-org/registry-proxy/internal/http"
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

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cachePurger := cache.NewCachePurger(logger, db, s3Storage, cfg)
	go cachePurger.Start(ctx)

	httpserver.StartServers(logger, router)

	handleGracefulShutdown()

	logger.Info("Server running on ports 8443 (HTTP) and 9443 (HTTPS)")
	select {}
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

	proxyHandler := handlers.NewProxyHandler(logger, cfg, storage, dhClient, db)
	handlers.RegisterRoutes(r, proxyHandler)
	return r
}

func handleGracefulShutdown() {
	sigint := make(chan os.Signal, 1)
	signal.Notify(sigint, syscall.SIGINT, syscall.SIGTERM)
	<-sigint

	logger.Info("Initiating graceful shutdown")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_ = ctx

	logger.Info("Server shutdown complete")
}
