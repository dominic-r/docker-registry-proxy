package config

import (
	"os"
	"strconv"
	"time"
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
}

func Load() *Config {
	cfg := &Config{
		S3Bucket:          getEnv("S3_BUCKET", "registry-cache"),
		S3Region:          getEnv("AWS_REGION", "us-east-1"),
		S3Endpoint:        mustGetEnv("S3_ENDPOINT"),
		S3AccessKey:       mustGetEnv("AWS_ACCESS_KEY_ID"),
		S3SecretKey:       mustGetEnv("AWS_SECRET_ACCESS_KEY"),
		DockerHubUser:     mustGetEnv("DOCKERHUB_USER"),
		DockerHubPassword: mustGetEnv("DOCKERHUB_PASSWORD"),
		CacheTTL:          getEnvDuration("CACHE_TTL", 12*time.Hour),
		RateLimit:         getEnvInt("RATE_LIMIT", 100),
		RateLimitWindow:   getEnvDuration("RATE_LIMIT_WINDOW", time.Minute),
		PostgresUser:      getEnv("POSTGRES_USER", "registry"),
		PostgresPassword:  getEnv("POSTGRES_PASSWORD", "password"),
		PostgresHost:      getEnv("POSTGRES_HOST", "localhost"),
		PostgresPort:      getEnv("POSTGRES_PORT", "5432"),
		PostgresDatabase:  getEnv("POSTGRES_DATABASE", "registry_proxy"),
		PostgresSSLMode:   getEnv("POSTGRES_SSL_MODE", "disable"),
	}

	if cfg.S3AccessKey == "" || cfg.S3SecretKey == "" || cfg.S3Endpoint == "" || cfg.DockerHubUser == "" || cfg.DockerHubPassword == "" {
		panic("AWS credentials must be provided")
	}

	return cfg
}

func mustGetEnv(key string) string {
	value := os.Getenv(key)
	if value == "" {
		panic("Missing required environment variable: " + key)
	}
	return value
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func getEnvInt(key string, defaultValue int) int {
	if value := os.Getenv(key); value != "" {
		if intValue, err := strconv.Atoi(value); err == nil {
			return intValue
		}
	}
	return defaultValue
}

func getEnvDuration(key string, defaultValue time.Duration) time.Duration {
	if value := os.Getenv(key); value != "" {
		if duration, err := time.ParseDuration(value); err == nil {
			return duration
		}
	}
	return defaultValue
}
