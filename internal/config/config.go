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
}

func Load() *Config {
	return &Config{
		S3Bucket:          getEnv("S3_BUCKET", "registry-cache"),
		S3Region:          getEnv("AWS_REGION", "us-east-1"),
		S3Endpoint:        getEnv("S3_ENDPOINT", ""),
		S3AccessKey:       getEnv("AWS_ACCESS_KEY_ID", ""),
		S3SecretKey:       getEnv("AWS_SECRET_ACCESS_KEY", ""),
		DockerHubUser:     os.Getenv("DOCKERHUB_USER"),
		DockerHubPassword: os.Getenv("DOCKERHUB_PASSWORD"),
		CacheTTL:          getEnvDuration("CACHE_TTL", 12*time.Hour),
		RateLimit:         getEnvInt("RATE_LIMIT", 100),
		RateLimitWindow:   getEnvDuration("RATE_LIMIT_WINDOW", time.Minute),
	}
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
