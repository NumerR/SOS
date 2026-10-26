package handlers

import (
	"encoding/json"
	"net/http"
)

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)

	if err := json.NewEncoder(w).Encode(value); err != nil {
		http.Error(w, "json encode error", http.StatusInternalServerError)
	}
}

func methodNotAllowed(w http.ResponseWriter) {
	writeError(w, http.StatusMethodNotAllowed, "method not allowed")
}

func readJSON(w http.ResponseWriter, r *http.Request, destination any) bool {
	defer r.Body.Close()

	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)

	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(destination); err != nil {
		// Не validation: клиент прислал не разбираемое тело, а не корректный
		// JSON с плохими значениями полей. Разница видна в коде, а не в тексте.
		writeCodeError(w, http.StatusBadRequest, ErrInvalidJSON, "invalid json")
		return false
	}

	return true
}
