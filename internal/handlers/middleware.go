package handlers

import (
	"log"
	"net/http"
	"time"
)

func HandleV2Check(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Docker-Distribution-API-Version", "registry/2.0")
	w.WriteHeader(http.StatusOK)
}

func LoggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		lrw := &loggingResponseWriter{ResponseWriter: w}

		defer func() {
			log.Printf("%s %s %d %s",
				r.Method,
				r.URL.Path,
				lrw.statusCode,
				time.Since(start),
			)
		}()

		next.ServeHTTP(lrw, r)
	})
}

type loggingResponseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (lrw *loggingResponseWriter) WriteHeader(code int) {
	lrw.statusCode = code
	lrw.ResponseWriter.WriteHeader(code)
}
