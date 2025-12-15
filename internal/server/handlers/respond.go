package handlers

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"
)

type apiError struct {
	Error string `json:"error"`
}

func respondJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if payload != nil {
		if err := json.NewEncoder(w).Encode(payload); err != nil {
			log.Printf("respondJSON encode error: %v", err)
		}
	}
}

func respondError(w http.ResponseWriter, status int, msg string) {
	respondJSON(w, status, apiError{Error: msg})
}

func methodNotAllowed(w http.ResponseWriter, allowed ...string) {
	if len(allowed) > 0 {
		w.Header().Set("Allow", strings.Join(allowed, ", "))
	}
	respondError(w, http.StatusMethodNotAllowed, "method not allowed")
}
