package handlers

import (
	"context"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/sdko-org/registry-proxy/internal/config"
	"github.com/sdko-org/registry-proxy/internal/models"
	"github.com/sirupsen/logrus"
	"golang.org/x/time/rate"
	"gorm.io/gorm"
)

type RateLimiter struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

type loggingResponseWriter struct {
	http.ResponseWriter
	statusCode int
	bytesSent  int
}

func (lrw *loggingResponseWriter) WriteHeader(code int) {
	lrw.statusCode = code
	lrw.ResponseWriter.WriteHeader(code)
}

func (lrw *loggingResponseWriter) Write(b []byte) (int, error) {
	n, err := lrw.ResponseWriter.Write(b)
	lrw.bytesSent += n
	return n, err
}

var (
	clients = make(map[string]*RateLimiter)
	mu      sync.Mutex
)

func LoggingMiddleware(logger *logrus.Logger, db *gorm.DB) func(http.Handler) http.Handler {
	logEntry := logger.WithField("component", "http_middleware")

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			lrw := &loggingResponseWriter{ResponseWriter: w, statusCode: http.StatusOK}

			defer func() {
				duration := time.Since(start)
				fields := logrus.Fields{
					"method":     r.Method,
					"path":       r.URL.Path,
					"status":     lrw.statusCode,
					"duration":   duration,
					"client_ip":  getClientIP(r),
					"bytes":      lrw.bytesSent,
					"user_agent": r.UserAgent(),
				}

				logEntry.WithFields(fields).Info("Request processed")

				go func() {
					ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
					defer cancel()

					entry := models.AccessLog{
						Timestamp: start,
						Method:    r.Method,
						Path:      r.URL.Path,
						Status:    lrw.statusCode,
						Duration:  duration,
						ClientIP:  getClientIP(r),
						UserAgent: r.UserAgent(),
						BytesSent: lrw.bytesSent,
					}

					if err := db.WithContext(ctx).Create(&entry).Error; err != nil {
						logEntry.WithError(err).Warn("Failed to save access log")
					}
				}()
			}()

			next.ServeHTTP(lrw, r)
		})
	}
}

func RateLimitMiddleware(cfg *config.Config) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			clientIP := getClientIP(r)

			mu.Lock()
			limiter, exists := clients[clientIP]
			if !exists {
				limiter = &RateLimiter{
					limiter: rate.NewLimiter(
						rate.Limit(float64(cfg.RateLimit)/cfg.RateLimitWindow.Seconds()),
						cfg.RateLimit,
					),
				}
				clients[clientIP] = limiter
			}
			limiter.lastSeen = time.Now()
			mu.Unlock()

			if !limiter.limiter.Allow() {
				http.Error(w, "Too many requests", http.StatusTooManyRequests)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

func getClientIP(r *http.Request) string {
	ip := r.Header.Get("X-Forwarded-For")
	if ip == "" {
		ip = r.Header.Get("X-Real-IP")
	}
	if ip == "" {
		var err error
		ip, _, err = net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			ip = r.RemoteAddr
		}
	}
	if strings.Contains(ip, ",") {
		parts := strings.Split(ip, ",")
		ip = strings.TrimSpace(parts[0])
	}
	return ip
}

func cleanupClients() {
	for {
		time.Sleep(time.Minute)
		mu.Lock()
		for ip, client := range clients {
			if time.Since(client.lastSeen) > 3*time.Minute {
				delete(clients, ip)
			}
		}
		mu.Unlock()
	}
}

func init() {
	go cleanupClients()
}
