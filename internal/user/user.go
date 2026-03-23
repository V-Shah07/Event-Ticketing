// Package user handles registration and login, issuing JWTs on success.
package user

import (
	"context"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/v-shah07/event-ticketing/internal/auth"
	"github.com/v-shah07/event-ticketing/internal/httpx"
)

type Store struct{ pool *pgxpool.Pool }

func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

type Record struct {
	ID   string
	Role auth.Role
	Hash string
}

func (s *Store) Create(ctx context.Context, email, hash string, role auth.Role) (string, error) {
	var id string
	err := s.pool.QueryRow(ctx,
		`INSERT INTO users (email, password_hash, role) VALUES ($1,$2,$3) RETURNING id`,
		email, hash, string(role),
	).Scan(&id)
	return id, err
}

func (s *Store) ByEmail(ctx context.Context, email string) (*Record, error) {
	r := &Record{}
	var role string
	err := s.pool.QueryRow(ctx,
		`SELECT id, role, password_hash FROM users WHERE email=$1`, email,
	).Scan(&r.ID, &role, &r.Hash)
	if err != nil {
		return nil, err
	}
	r.Role = auth.Role(role)
	return r, nil
}

type Handler struct {
	Store *Store
	JWT   *auth.Manager
}

func NewHandler(s *Store, jwt *auth.Manager) *Handler { return &Handler{Store: s, JWT: jwt} }

type credentials struct {
	Email    string    `json:"email"`
	Password string    `json:"password"`
	Role     auth.Role `json:"role"`
}

type tokenResponse struct {
	Token  string    `json:"token"`
	UserID string    `json:"user_id"`
	Role   auth.Role `json:"role"`
}

func (h *Handler) Register(w http.ResponseWriter, r *http.Request) {
	var c credentials
	if err := httpx.Decode(r, &c); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid body")
		return
	}
	if c.Email == "" || len(c.Password) < 6 {
		httpx.Error(w, http.StatusBadRequest, "email and password (>=6 chars) required")
		return
	}
	role := c.Role
	if role != auth.RoleOrganizer && role != auth.RoleAdmin {
		role = auth.RoleBuyer
	}
	hash, err := auth.HashPassword(c.Password)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	id, err := h.Store.Create(r.Context(), c.Email, hash, role)
	if err != nil {
		httpx.Error(w, http.StatusConflict, "email already registered")
		return
	}
	tok, err := h.JWT.Issue(id, role)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.JSON(w, http.StatusCreated, tokenResponse{Token: tok, UserID: id, Role: role})
}

func (h *Handler) Login(w http.ResponseWriter, r *http.Request) {
	var c credentials
	if err := httpx.Decode(r, &c); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid body")
		return
	}
	rec, err := h.Store.ByEmail(r.Context(), c.Email)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !auth.CheckPassword(rec.Hash, c.Password)) {
		httpx.Error(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	tok, err := h.JWT.Issue(rec.ID, rec.Role)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.JSON(w, http.StatusOK, tokenResponse{Token: tok, UserID: rec.ID, Role: rec.Role})
}
