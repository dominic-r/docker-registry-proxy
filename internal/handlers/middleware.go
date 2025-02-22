package handlers

import (
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/sdko-org/registry-proxy/internal/models"
	"gorm.io/gorm"
)

func HandleV2Check(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Docker-Distribution-API-Version", "registry/2.0")
	w.WriteHeader(http.StatusOK)
}

func LoggingMiddleware(db *gorm.DB) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			lrw := &loggingResponseWriter{ResponseWriter: w}

			defer func() {
				duration := time.Since(start)
				logEntry := models.AccessLog{
					Timestamp: start,
					Method:    r.Method,
					Path:      r.URL.Path,
					Status:    lrw.statusCode,
					Duration:  duration,
					ClientIP:  getClientIP(r),
					UserAgent: r.UserAgent(),
					BytesSent: lrw.bytesSent,
				}

				if err := db.Create(&logEntry).Error; err != nil {
					log.Printf("Failed to save access log: %v", err)
				}
			}()

			next.ServeHTTP(lrw, r)
		})
	}
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

func getClientIP(r *http.Request) string {
	ip := r.Header.Get("X-Forwarded-For")
	if ip == "" {
		ip = r.Header.Get("X-Real-IP")
	}
	if ip == "" {
		ip, _, _ = net.SplitHostPort(r.RemoteAddr)
	}
	if strings.Contains(ip, ",") {
		parts := strings.Split(ip, ",")
		ip = strings.TrimSpace(parts[0])
	}
	return ip
}
