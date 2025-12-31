package handlers

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"my-personal-budget/internal/auth"
	"my-personal-budget/internal/config"
	"my-personal-budget/internal/passkey"
	"my-personal-budget/internal/store"

	"github.com/go-webauthn/webauthn/webauthn"
)

// APIHandler bundles top-level API routes to keep the main router small.
type APIHandler struct {
	cfg     config.Config
	store   BudgetStore
	pass    *passkey.ChallengeStore
	webAuth *webauthn.WebAuthn
}

// BudgetStore abstracts data access for testability.
type BudgetStore interface {
	ListBudgets(ctx context.Context, userID *int64) ([]store.Budget, error)
	GetBudget(ctx context.Context, id int64, userID *int64) (store.Budget, error)
	CreateBudget(ctx context.Context, userID *int64, name string, payroll float64) (store.Budget, error)
	UpdateBudget(ctx context.Context, id int64, userID *int64, name string, payroll float64) (store.Budget, error)
	DeleteBudget(ctx context.Context, id int64, userID *int64) error
	ListTransactions(ctx context.Context, budgetID int64, userID *int64, limit int) ([]store.Transaction, error)
	ListTransactionsPaged(ctx context.Context, budgetID int64, userID *int64, limit, offset int, search string) ([]store.Transaction, error)
	CreateTransaction(ctx context.Context, budgetID int64, userID *int64, description string, credit bool, amount float64) (store.Transaction, error)
	UpdateTransaction(ctx context.Context, budgetID, transactionID int64, userID *int64, description string, credit bool, amount float64) (store.Transaction, error)
	DeleteTransaction(ctx context.Context, budgetID, transactionID int64, userID *int64) error
	GetUserByEmail(ctx context.Context, email string) (store.User, error)
	GetUserByID(ctx context.Context, id int64) (store.User, error)
	GetOrCreateUser(ctx context.Context, email string) (store.User, error)
	ListBudgetShares(ctx context.Context, budgetID int64, userID *int64) ([]store.User, error)
	AddBudgetShare(ctx context.Context, budgetID int64, ownerID *int64, email string) (store.User, error)
	RemoveBudgetShare(ctx context.Context, budgetID int64, ownerID *int64, email string) error
	GetPasskeyByUser(ctx context.Context, userID int64) (store.Passkey, error)
	GetPasskeyByCredentialID(ctx context.Context, credentialID string) (store.Passkey, error)
	CreatePasskey(ctx context.Context, userID int64, credentialID, publicKey string, signCount int, backupEligible, backupState bool) (store.Passkey, error)
	UpdatePasskeySignCount(ctx context.Context, credentialID string, signCount int) error
}

func NewAPIHandler(cfg config.Config, store BudgetStore, pass *passkey.ChallengeStore) (*APIHandler, error) {
	w, err := webauthn.New(&webauthn.Config{
		RPDisplayName: cfg.RelyingPartyName,
		RPID:          cfg.RelyingPartyID,
		RPOrigins:     rpOrigins(cfg),
	})
	if err != nil {
		return nil, err
	}
	return &APIHandler{cfg: cfg, store: store, pass: pass, webAuth: w}, nil
}

// Router returns a mux containing versioned API routes.
func (h *APIHandler) Router() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", h.index)
	mux.HandleFunc("/budgets", h.handleBudgets)
	mux.HandleFunc("/budgets/", h.handleBudgetByID)
	return mux
}

// PasskeyBeginHandler starts a passkey registration ceremony.
func (h *APIHandler) PasskeyBeginHandler() http.Handler {
	return http.HandlerFunc(h.passkeyBegin)
}

// PasskeyFinishHandler completes a passkey registration ceremony (demo only).
func (h *APIHandler) PasskeyFinishHandler() http.Handler {
	return http.HandlerFunc(h.passkeyFinish)
}

// PasskeyLoginBeginHandler starts an assertion ceremony.
func (h *APIHandler) PasskeyLoginBeginHandler() http.Handler {
	return http.HandlerFunc(h.passkeyLoginBegin)
}

// PasskeyLoginFinishHandler completes an assertion ceremony.
func (h *APIHandler) PasskeyLoginFinishHandler() http.Handler {
	return http.HandlerFunc(h.passkeyLoginFinish)
}

func (h *APIHandler) index(w http.ResponseWriter, r *http.Request) {
	resp := map[string]any{
		"message": "Go API for My Personal Budget",
		"version": "v1",
		"time":    time.Now().UTC(),
	}
	respondJSON(w, http.StatusOK, resp)
}

func (h *APIHandler) requireUser(w http.ResponseWriter, r *http.Request) (*int64, bool) {
	userID := auth.UserIDFromContext(r.Context())
	if h.cfg.JWTSecret != "" && userID == nil {
		respondError(w, http.StatusUnauthorized, "unauthorized")
		return nil, false
	}
	return userID, true
}

// passkeyBegin returns a basic PublicKeyCredentialCreationOptions payload for demo.
func (h *APIHandler) passkeyBegin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	var req struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Email) == "" {
		respondError(w, http.StatusBadRequest, "email required")
		return
	}
	req.Email = strings.ToLower(strings.TrimSpace(req.Email))
	user, err := h.store.GetOrCreateUser(r.Context(), req.Email)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to ensure user")
		return
	}
	if _, err := h.store.GetPasskeyByUser(r.Context(), user.ID); err == nil {
		respondError(w, http.StatusConflict, "passkey already registered for this email")
		return
	} else if err != nil && !errors.Is(err, store.ErrNotFound) {
		respondError(w, http.StatusInternalServerError, "failed to check existing passkey")
		return
	}

	waUser := webUserFrom(user, nil)
	opts, sessionData, err := h.webAuth.BeginRegistration(waUser)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to start registration")
		return
	}
	h.pass.SaveRegistrationSession(req.Email, *sessionData)
	respondJSON(w, http.StatusOK, opts)
}

// passkeyFinish accepts the response and stores a placeholder credential (no real attestation verification; demo only).
func (h *APIHandler) passkeyFinish(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	if h.cfg.JWTSecret == "" {
		respondError(w, http.StatusInternalServerError, "JWT_SECRET not configured")
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		respondError(w, http.StatusBadRequest, "invalid payload")
		return
	}
	r.Body.Close()
	r.Body = io.NopCloser(bytes.NewReader(body))

	var req struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(bytes.NewReader(body)).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid payload")
		return
	}
	req.Email = strings.ToLower(strings.TrimSpace(req.Email))
	if req.Email == "" {
		respondError(w, http.StatusBadRequest, "email required")
		return
	}
	sessionData, ok := h.pass.ConsumeRegistrationSession(req.Email)
	if !ok {
		respondError(w, http.StatusUnauthorized, "registration session expired")
		return
	}
	user, err := h.store.GetUserByEmail(r.Context(), req.Email)
	if errors.Is(err, store.ErrNotFound) {
		respondError(w, http.StatusUnauthorized, "user not found")
		return
	}
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to load user")
		return
	}
	if _, err := h.store.GetPasskeyByUser(r.Context(), user.ID); err == nil {
		respondError(w, http.StatusConflict, "passkey already registered for this email")
		return
	} else if err != nil && !errors.Is(err, store.ErrNotFound) {
		respondError(w, http.StatusInternalServerError, "failed to check existing passkey")
		return
	}

	waUser := webUserFrom(user, nil)
	cred, err := h.webAuth.FinishRegistration(waUser, sessionData, r)
	if err != nil {
		respondError(w, http.StatusUnauthorized, "registration verification failed")
		return
	}
	credID := base64.RawURLEncoding.EncodeToString(cred.ID)
	pubKey := base64.RawURLEncoding.EncodeToString(cred.PublicKey)
	if _, err := h.store.CreatePasskey(
		r.Context(),
		user.ID,
		credID,
		pubKey,
		int(cred.Authenticator.SignCount),
		cred.Flags.BackupEligible,
		cred.Flags.BackupState,
	); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			respondError(w, http.StatusConflict, "passkey already registered")
			return
		}
		respondError(w, http.StatusInternalServerError, "failed to store passkey")
		return
	}
	token, err := auth.GenerateToken(user.ID, h.cfg.JWTSecret, 24*time.Hour)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to generate token")
		return
	}
	respondJSON(w, http.StatusCreated, map[string]any{
		"status": "registered",
		"token":  token,
		"user":   map[string]any{"id": user.ID, "email": user.Email},
	})
}

func (h *APIHandler) passkeyLoginBegin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	opts, sessionData, err := h.webAuth.BeginDiscoverableLogin()
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to start login")
		return
	}
	sessionID, err := h.pass.SaveAuthSessionByID(*sessionData)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to start login")
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{
		"session_id": sessionID,
		"publicKey":  opts.Response,
	})
}

func (h *APIHandler) passkeyLoginFinish(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	if h.cfg.JWTSecret == "" {
		respondError(w, http.StatusInternalServerError, "JWT_SECRET not configured")
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		respondError(w, http.StatusBadRequest, "invalid payload")
		return
	}
	r.Body.Close()
	r.Body = io.NopCloser(bytes.NewReader(body))

	var req struct {
		SessionID string `json:"session_id"`
	}
	if err := json.NewDecoder(bytes.NewReader(body)).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid payload")
		return
	}
	req.SessionID = strings.TrimSpace(req.SessionID)
	if req.SessionID == "" {
		respondError(w, http.StatusBadRequest, "session id required")
		return
	}
	sessionData, ok := h.pass.ConsumeAuthSessionByID(req.SessionID)
	if !ok {
		respondError(w, http.StatusUnauthorized, "login session expired")
		return
	}

	handler := func(rawID, userHandle []byte) (webauthn.User, error) {
		if len(rawID) > 0 {
			credentialID := base64.RawURLEncoding.EncodeToString(rawID)
			passkey, err := h.store.GetPasskeyByCredentialID(r.Context(), credentialID)
			if err == nil {
				user, err := h.store.GetUserByID(r.Context(), passkey.UserID)
				if err != nil {
					return nil, err
				}
				return webUserFrom(user, []store.Passkey{passkey}), nil
			}
			if !errors.Is(err, store.ErrNotFound) {
				return nil, err
			}
		}
		if len(userHandle) > 0 {
			userID, err := strconv.ParseInt(string(userHandle), 10, 64)
			if err == nil {
				user, err := h.store.GetUserByID(r.Context(), userID)
				if err != nil {
					return nil, err
				}
				passkey, err := h.store.GetPasskeyByUser(r.Context(), user.ID)
				if err != nil {
					return nil, err
				}
				return webUserFrom(user, []store.Passkey{passkey}), nil
			}
		}
		return nil, store.ErrNotFound
	}

	_, cred, err := h.webAuth.FinishPasskeyLogin(handler, sessionData, r)
	if err != nil {
		// Log the underlying error for troubleshooting; response remains generic.
		log.Printf("passkey finish failed: %v", err)
		respondError(w, http.StatusUnauthorized, "authentication failed")
		return
	}
	credID := base64.RawURLEncoding.EncodeToString(cred.ID)
	passkey, err := h.store.GetPasskeyByCredentialID(r.Context(), credID)
	if errors.Is(err, store.ErrNotFound) {
		respondError(w, http.StatusUnauthorized, "credential not found")
		return
	}
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to load passkey")
		return
	}
	user, err := h.store.GetUserByID(r.Context(), passkey.UserID)
	if errors.Is(err, store.ErrNotFound) {
		respondError(w, http.StatusUnauthorized, "user not found")
		return
	}
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to load user")
		return
	}
	if err := h.store.UpdatePasskeySignCount(r.Context(), credID, int(cred.Authenticator.SignCount)); err != nil && !errors.Is(err, store.ErrNotFound) {
		respondError(w, http.StatusInternalServerError, "failed to update passkey")
		return
	}
	token, err := auth.GenerateToken(user.ID, h.cfg.JWTSecret, 24*time.Hour)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to generate token")
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{
		"token": token,
		"user":  map[string]any{"id": user.ID, "email": user.Email},
	})
}

func (h *APIHandler) listShares(w http.ResponseWriter, r *http.Request, budgetID int64, userID *int64) {
	shares, err := h.store.ListBudgetShares(r.Context(), budgetID, userID)
	if errors.Is(err, store.ErrNotFound) {
		respondError(w, http.StatusNotFound, "budget not found")
		return
	}
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to list shares")
		return
	}
	type share struct {
		ID    int64  `json:"id"`
		Email string `json:"email"`
	}
	out := make([]share, 0, len(shares))
	for _, u := range shares {
		out = append(out, share{ID: u.ID, Email: u.Email})
	}
	respondJSON(w, http.StatusOK, map[string]any{"data": out})
}

func (h *APIHandler) addShare(w http.ResponseWriter, r *http.Request, budgetID int64, ownerID *int64) {
	var req struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Email) == "" {
		respondError(w, http.StatusBadRequest, "email required")
		return
	}
	user, err := h.store.AddBudgetShare(r.Context(), budgetID, ownerID, strings.ToLower(strings.TrimSpace(req.Email)))
	if errors.Is(err, store.ErrNotFound) {
		respondError(w, http.StatusNotFound, "budget not found")
		return
	}
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to add share")
		return
	}
	respondJSON(w, http.StatusCreated, map[string]any{"id": user.ID, "email": user.Email})
}

func (h *APIHandler) removeShare(w http.ResponseWriter, r *http.Request, budgetID int64, ownerID *int64) {
	var req struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Email) == "" {
		respondError(w, http.StatusBadRequest, "email required")
		return
	}
	err := h.store.RemoveBudgetShare(r.Context(), budgetID, ownerID, strings.ToLower(strings.TrimSpace(req.Email)))
	if errors.Is(err, store.ErrNotFound) {
		respondError(w, http.StatusNotFound, "budget or user not found")
		return
	}
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to remove share")
		return
	}
	respondJSON(w, http.StatusNoContent, nil)
}

func stripPort(host string) string {
	if host == "" {
		return host
	}
	if strings.Contains(host, ":") {
		parts := strings.Split(host, ":")
		return parts[0]
	}
	return host
}

type webUser struct {
	id          []byte
	email       string
	creds       []webauthn.Credential
	displayName string
}

func (u *webUser) WebAuthnID() []byte {
	return u.id
}

func (u *webUser) WebAuthnName() string {
	return u.email
}

func (u *webUser) WebAuthnDisplayName() string {
	if u.displayName != "" {
		return u.displayName
	}
	return u.email
}

func (u *webUser) WebAuthnIcon() string {
	return ""
}

func (u *webUser) WebAuthnCredentials() []webauthn.Credential {
	return u.creds
}

func webUserFrom(user store.User, passkeys []store.Passkey) *webUser {
	creds := make([]webauthn.Credential, 0, len(passkeys))
	for _, pk := range passkeys {
		credID, err := decodeBase64URL(pk.CredentialID)
		if err != nil {
			continue
		}
		pubKey, err := decodeBase64URL(pk.PublicKey)
		if err != nil {
			continue
		}
		creds = append(creds, webauthn.Credential{
			ID:        credID,
			PublicKey: pubKey,
			Flags: webauthn.CredentialFlags{
				BackupEligible: pk.BackupEligible,
				BackupState:    pk.BackupState,
			},
			Authenticator: webauthn.Authenticator{
				SignCount: uint32(pk.SignCount),
			},
		})
	}
	return &webUser{
		id:          []byte(strconv.FormatInt(user.ID, 10)),
		email:       user.Email,
		creds:       creds,
		displayName: user.Email,
	}
}

func rpOrigins(cfg config.Config) []string {
	if len(cfg.AllowedOrigins) > 0 {
		return cfg.AllowedOrigins
	}
	return []string{
		"https://" + cfg.RelyingPartyID,
		"http://" + cfg.RelyingPartyID,
	}
}

func decodeBase64URL(value string) ([]byte, error) {
	if value == "" {
		return nil, errors.New("empty base64")
	}
	if b, err := base64.RawURLEncoding.DecodeString(value); err == nil {
		return b, nil
	}
	return base64.URLEncoding.DecodeString(value)
}

func (h *APIHandler) handleBudgets(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireUser(w, r); !ok {
		return
	}

	switch r.Method {
	case http.MethodGet:
		h.listBudgets(w, r)
	case http.MethodPost:
		h.createBudget(w, r)
	default:
		methodNotAllowed(w, http.MethodGet, http.MethodPost)
	}
}

func (h *APIHandler) listBudgets(w http.ResponseWriter, r *http.Request) {
	userID, _ := h.requireUser(w, r)
	budgets, err := h.store.ListBudgets(r.Context(), userID)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to list budgets")
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{
		"data": budgets,
		"meta": map[string]any{"count": len(budgets)},
	})
}

func (h *APIHandler) createBudget(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name    string  `json:"name"`
		Payroll float64 `json:"payroll"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid JSON payload")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		respondError(w, http.StatusBadRequest, "name is required")
		return
	}
	if req.Payroll < 0 {
		respondError(w, http.StatusBadRequest, "payroll must be >= 0")
		return
	}

	userID, _ := h.requireUser(w, r)

	budget, err := h.store.CreateBudget(r.Context(), userID, req.Name, req.Payroll)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to create budget")
		return
	}
	respondJSON(w, http.StatusCreated, budget)
}

func (h *APIHandler) handleBudgetByID(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.requireUser(w, r)
	if !ok {
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/budgets/")
	if path == "" {
		respondError(w, http.StatusNotFound, "not found")
		return
	}
	parts := strings.Split(path, "/")
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		respondError(w, http.StatusBadRequest, "invalid budget id")
		return
	}

	if len(parts) == 1 {
		switch r.Method {
		case http.MethodGet:
			h.getBudget(w, r, id, userID)
		case http.MethodPut, http.MethodPatch:
			h.updateBudget(w, r, id, userID)
		case http.MethodDelete:
			h.deleteBudget(w, r, id, userID)
		default:
			methodNotAllowed(w, http.MethodGet, http.MethodPut, http.MethodPatch, http.MethodDelete)
		}
		return
	}

	if len(parts) == 2 && parts[1] == "transactions" {
		switch r.Method {
		case http.MethodGet:
			h.listTransactions(w, r, id, userID)
		case http.MethodPost:
			h.createTransaction(w, r, id, userID)
		default:
			methodNotAllowed(w, http.MethodGet, http.MethodPost)
		}
		return
	}

	if len(parts) == 3 && parts[1] == "transactions" {
		txnID, err := strconv.ParseInt(parts[2], 10, 64)
		if err != nil {
			respondError(w, http.StatusBadRequest, "invalid transaction id")
			return
		}
		switch r.Method {
		case http.MethodPut, http.MethodPatch:
			h.updateTransaction(w, r, id, txnID, userID)
		case http.MethodDelete:
			h.deleteTransaction(w, r, id, txnID, userID)
		default:
			methodNotAllowed(w, http.MethodPut, http.MethodPatch, http.MethodDelete)
		}
		return
	}

	if len(parts) == 2 && parts[1] == "shares" {
		switch r.Method {
		case http.MethodGet:
			h.listShares(w, r, id, userID)
		case http.MethodPost:
			h.addShare(w, r, id, userID)
		case http.MethodDelete:
			h.removeShare(w, r, id, userID)
		default:
			methodNotAllowed(w, http.MethodGet, http.MethodPost, http.MethodDelete)
		}
		return
	}

	respondError(w, http.StatusNotFound, "not found")
}

func (h *APIHandler) getBudget(w http.ResponseWriter, r *http.Request, id int64, userID *int64) {
	budget, err := h.store.GetBudget(r.Context(), id, userID)
	if errors.Is(err, store.ErrNotFound) {
		respondError(w, http.StatusNotFound, "budget not found")
		return
	}
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to load budget")
		return
	}
	respondJSON(w, http.StatusOK, budget)
}

func (h *APIHandler) updateBudget(w http.ResponseWriter, r *http.Request, id int64, userID *int64) {
	var req struct {
		Name    string  `json:"name"`
		Payroll float64 `json:"payroll"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid JSON payload")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		respondError(w, http.StatusBadRequest, "name is required")
		return
	}
	if req.Payroll < 0 {
		respondError(w, http.StatusBadRequest, "payroll must be >= 0")
		return
	}

	budget, err := h.store.UpdateBudget(r.Context(), id, userID, req.Name, req.Payroll)
	if errors.Is(err, store.ErrNotFound) {
		respondError(w, http.StatusNotFound, "budget not found")
		return
	}
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to update budget")
		return
	}
	respondJSON(w, http.StatusOK, budget)
}

func (h *APIHandler) deleteBudget(w http.ResponseWriter, r *http.Request, id int64, userID *int64) {
	err := h.store.DeleteBudget(r.Context(), id, userID)
	if errors.Is(err, store.ErrNotFound) {
		respondError(w, http.StatusNotFound, "budget not found")
		return
	}
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to delete budget")
		return
	}
	respondJSON(w, http.StatusNoContent, nil)
}

func (h *APIHandler) listTransactions(w http.ResponseWriter, r *http.Request, budgetID int64, userID *int64) {
	q := r.URL.Query()
	limit := 50
	if l := q.Get("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil {
			limit = parsed
		}
	}
	offset := 0
	if o := q.Get("offset"); o != "" {
		if parsed, err := strconv.Atoi(o); err == nil {
			offset = parsed
		}
	}
	search := strings.TrimSpace(q.Get("q"))

	txns, err := h.store.ListTransactionsPaged(r.Context(), budgetID, userID, limit, offset, search)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			respondError(w, http.StatusNotFound, "budget not found")
			return
		}
		respondError(w, http.StatusInternalServerError, "failed to list transactions")
		return
	}
	nextOffset := offset + len(txns)
	hasMore := len(txns) == limit
	respondJSON(w, http.StatusOK, map[string]any{
		"data": txns,
		"meta": map[string]any{
			"count":      len(txns),
			"offset":     offset,
			"nextOffset": nextOffset,
			"hasMore":    hasMore,
		},
	})
}

func (h *APIHandler) createTransaction(w http.ResponseWriter, r *http.Request, budgetID int64, userID *int64) {
	var req struct {
		Description string  `json:"description"`
		Credit      bool    `json:"credit"`
		Amount      float64 `json:"amount"`
		UserID      *int64  `json:"user_id"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid JSON payload")
		return
	}
	req.Description = strings.TrimSpace(req.Description)
	if req.Description == "" {
		respondError(w, http.StatusBadRequest, "description is required")
		return
	}
	if req.Amount <= 0 {
		respondError(w, http.StatusBadRequest, "amount must be greater than 0")
		return
	}

	txn, err := h.store.CreateTransaction(r.Context(), budgetID, userID, req.Description, req.Credit, req.Amount)
	if errors.Is(err, store.ErrNotFound) {
		respondError(w, http.StatusNotFound, "budget not found")
		return
	}
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to create transaction")
		return
	}
	respondJSON(w, http.StatusCreated, txn)
}

func (h *APIHandler) updateTransaction(w http.ResponseWriter, r *http.Request, budgetID, txnID int64, userID *int64) {
	var req struct {
		Description string  `json:"description"`
		Credit      bool    `json:"credit"`
		Amount      float64 `json:"amount"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid JSON payload")
		return
	}
	req.Description = strings.TrimSpace(req.Description)
	if req.Description == "" {
		respondError(w, http.StatusBadRequest, "description is required")
		return
	}
	if req.Amount <= 0 {
		respondError(w, http.StatusBadRequest, "amount must be greater than 0")
		return
	}

	txn, err := h.store.UpdateTransaction(r.Context(), budgetID, txnID, userID, req.Description, req.Credit, req.Amount)
	if errors.Is(err, store.ErrNotFound) {
		respondError(w, http.StatusNotFound, "transaction not found")
		return
	}
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to update transaction")
		return
	}
	respondJSON(w, http.StatusOK, txn)
}

func (h *APIHandler) deleteTransaction(w http.ResponseWriter, r *http.Request, budgetID, txnID int64, userID *int64) {
	err := h.store.DeleteTransaction(r.Context(), budgetID, txnID, userID)
	if errors.Is(err, store.ErrNotFound) {
		respondError(w, http.StatusNotFound, "transaction not found")
		return
	}
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to delete transaction")
		return
	}
	respondJSON(w, http.StatusNoContent, nil)
}
