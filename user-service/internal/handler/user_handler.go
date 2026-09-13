package handler

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"user-service/internal/repository"
	"user-service/internal/services"
)

type UserHandler struct {
	service *services.UserService
}

func NewUserHandler(service *services.UserService) *UserHandler {
	return &UserHandler{service: service}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(body)
}

// GetUsers handles GET /users
func (h *UserHandler) GetUsers(w http.ResponseWriter, r *http.Request) {
	users, err := h.service.GetUsers(r.Context())
	if err != nil {
		log.Printf("GetUsers failed: %v\n", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"message": "Failed to fetch users"})
		return
	}

	writeJSON(w, http.StatusOK, users)
}

// GetUserByID handles GET /users/{id}
func (h *UserHandler) GetUserByID(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	user, err := h.service.GetUserByID(r.Context(), id)
	if errors.Is(err, repository.ErrUserNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "User not found"})
		return
	}
	if err != nil {
		log.Printf("GetUserByID(%s) failed: %v\n", id, err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"message": "Failed to fetch user"})
		return
	}

	writeJSON(w, http.StatusOK, user)
}
