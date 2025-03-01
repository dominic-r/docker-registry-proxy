package config

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/sirupsen/logrus"
)

type Config struct {
	S3Bucket          string
	S3Region          string
	S3Endpoint        string
	S3AccessKey       string
	S3SecretKey       string
	DockerHubUser     string
	DockerHubPassword string
	CacheTTL          time.Duration
	RateLimit         int
	RateLimitWindow   time.Duration
	PostgresUser      string
	PostgresPassword  string
	PostgresHost      string
	PostgresPort      string
	PostgresDatabase  string
	PostgresSSLMode   string
	TempDir           string
}

func Load(log *logrus.Logger) (*Config, error) {
	cfg := &Config{
		S3Bucket:          getEnv("S3_BUCKET", "registry-cache"),
		S3Region:          getEnv("AWS_REGION", "us-east-1"),
		S3Endpoint:        mustGetEnv(log, "S3_ENDPOINT"),
		S3AccessKey:       mustGetEnv(log, "AWS_ACCESS_KEY_ID"),
		S3SecretKey:       mustGetEnv(log, "AWS_SECRET_ACCESS_KEY"),
		DockerHubUser:     mustGetEnv(log, "DOCKERHUB_USER"),
		DockerHubPassword: mustGetEnv(log, "DOCKERHUB_PASSWORD"),
		CacheTTL:          getEnvDuration(log, "CACHE_TTL", 12*time.Hour),
		RateLimit:         getEnvInt(log, "RATE_LIMIT", 100),
		RateLimitWindow:   getEnvDuration(log, "RATE_LIMIT_WINDOW", time.Minute),
		PostgresUser:      getEnv("POSTGRES_USER", "registry"),
		PostgresPassword:  getEnv("POSTGRES_PASSWORD", "password"),
		PostgresHost:      getEnv("POSTGRES_HOST", "localhost"),
		PostgresPort:      getEnv("POSTGRES_PORT", "5432"),
		PostgresDatabase:  getEnv("POSTGRES_DATABASE", "registry_proxy"),
		PostgresSSLMode:   getEnv("POSTGRES_SSL_MODE", "disable"),
		TempDir:           getEnv("TEMP_DIR", "/tmp/registry-proxy"),
	}

	if cfg.S3AccessKey == "" || cfg.S3SecretKey == "" || cfg.S3Endpoint == "" {
		return nil, fmt.Errorf("AWS credentials must be provided")
	}

	return cfg, nil
}

func mustGetEnv(log *logrus.Logger, key string) string {
	value := os.Getenv(key)
	if value == "" {
		log.WithField("variable", key).Fatal("Missing required environment variable")
	}
	return value
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func getEnvInt(log *logrus.Logger, key string, defaultValue int) int {
	value := os.Getenv(key)
	if value == "" {
		return defaultValue
	}

	intValue, err := strconv.Atoi(value)
	if err != nil {
		log.WithFields(logrus.Fields{
			"variable": key,
			"value":    value,
		}).Warn("Invalid integer value, using default")
		return defaultValue
	}
	return intValue
}

func getEnvDuration(log *logrus.Logger, key string, defaultValue time.Duration) time.Duration {
	value := os.Getenv(key)
	if value == "" {
		return defaultValue
	}

	duration, err := time.ParseDuration(value)
	if err != nil {
		log.WithFields(logrus.Fields{
			"variable": key,
			"value":    value,
		}).Warn("Invalid duration format, using default")
		return defaultValue
	}
	return duration
}
