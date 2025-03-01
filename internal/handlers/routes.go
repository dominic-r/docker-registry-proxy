package handlers

import (
	"github.com/gorilla/mux"
)

func RegisterRoutes(r *mux.Router, ph *ProxyHandler) {
	r.HandleFunc("/v2/", HandleV2Check).Methods("GET")
	r.HandleFunc("/v2/_catalog", HandleCatalog).Methods("GET")
	r.HandleFunc("/admin/cache/invalidate", ph.InvalidateCache).Methods("POST")
	r.PathPrefix("/v2/").Handler(ph)
}
