package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"my-personal-budget/internal/config"
	"my-personal-budget/internal/passkey"
	"my-personal-budget/internal/store"
)

type fakeStore struct {
	budgets       []store.Budget
	createdBudget *store.Budget
	user          *store.User
	passkey       *store.Passkey
}

func (f *fakeStore) ListBudgets(ctx context.Context, userID *int64) ([]store.Budget, error) {
	return f.budgets, nil
}

func (f *fakeStore) GetBudget(ctx context.Context, id int64, userID *int64) (store.Budget, error) {
	for _, b := range f.budgets {
		if b.ID == id {
			return b, nil
		}
	}
	return store.Budget{}, store.ErrNotFound
}

func (f *fakeStore) CreateBudget(ctx context.Context, userID *int64, name string, payroll float64) (store.Budget, error) {
	b := store.Budget{
		ID:        int64(len(f.budgets) + 1),
		Name:      name,
		Payroll:   payroll,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	f.createdBudget = &b
	return b, nil
}

func (f *fakeStore) UpdateBudget(ctx context.Context, id int64, userID *int64, name string, payroll float64) (store.Budget, error) {
	return store.Budget{}, store.ErrNotFound
}

func (f *fakeStore) DeleteBudget(ctx context.Context, id int64, userID *int64) error {
	return store.ErrNotFound
}

func (f *fakeStore) ListTransactions(ctx context.Context, budgetID int64, userID *int64, limit int) ([]store.Transaction, error) {
	return nil, nil
}

func (f *fakeStore) ListTransactionsPaged(ctx context.Context, budgetID int64, userID *int64, limit, offset int, search string) ([]store.Transaction, error) {
	return nil, nil
}

func (f *fakeStore) CreateTransaction(ctx context.Context, budgetID int64, userID *int64, description string, credit bool, amount float64) (store.Transaction, error) {
	return store.Transaction{}, store.ErrNotFound
}

func (f *fakeStore) UpdateTransaction(ctx context.Context, budgetID, transactionID int64, userID *int64, description string, credit bool, amount float64) (store.Transaction, error) {
	return store.Transaction{}, store.ErrNotFound
}

func (f *fakeStore) DeleteTransaction(ctx context.Context, budgetID, transactionID int64, userID *int64) error {
	return store.ErrNotFound
}

func (f *fakeStore) GetUserByEmail(ctx context.Context, email string) (store.User, error) {
	if f.user != nil && strings.EqualFold(f.user.Email, email) {
		return *f.user, nil
	}
	return store.User{}, store.ErrNotFound
}

func (f *fakeStore) GetOrCreateUser(ctx context.Context, email string) (store.User, error) {
	if f.user != nil && strings.EqualFold(f.user.Email, email) {
		return *f.user, nil
	}
	u := store.User{ID: 1, Email: email}
	f.user = &u
	return u, nil
}

func (f *fakeStore) ListBudgetShares(ctx context.Context, budgetID int64, userID *int64) ([]store.User, error) {
	return nil, nil
}

func (f *fakeStore) AddBudgetShare(ctx context.Context, budgetID int64, ownerID *int64, email string) (store.User, error) {
	return store.User{ID: 2, Email: email}, nil
}

func (f *fakeStore) RemoveBudgetShare(ctx context.Context, budgetID int64, ownerID *int64, email string) error {
	return nil
}

func (f *fakeStore) GetPasskeyByUser(ctx context.Context, userID int64) (store.Passkey, error) {
	if f.passkey != nil && f.passkey.UserID == userID {
		return *f.passkey, nil
	}
	return store.Passkey{}, store.ErrNotFound
}

func (f *fakeStore) CreatePasskey(ctx context.Context, userID int64, credentialID, publicKey string, signCount int, backupEligible, backupState bool) (store.Passkey, error) {
	pk := store.Passkey{
		ID:             1,
		UserID:         userID,
		CredentialID:   credentialID,
		PublicKey:      publicKey,
		SignCount:      signCount,
		BackupEligible: backupEligible,
		BackupState:    backupState,
	}
	f.passkey = &pk
	return pk, nil
}

func (f *fakeStore) UpdatePasskeySignCount(ctx context.Context, credentialID string, signCount int) error {
	if f.passkey != nil && f.passkey.CredentialID == credentialID {
		f.passkey.SignCount = signCount
		return nil
	}
	return store.ErrNotFound
}

func TestListBudgets(t *testing.T) {
	fs := &fakeStore{
		budgets: []store.Budget{
			{ID: 1, Name: "Household", Payroll: 1000, CreatedAt: time.Now(), UpdatedAt: time.Now()},
		},
	}
	handler, err := NewAPIHandler(config.Config{}, fs, passkey.NewChallengeStore())
	if err != nil {
		t.Fatalf("NewAPIHandler error: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/budgets", nil)
	w := httptest.NewRecorder()
	handler.Router().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp["data"] == nil {
		t.Fatalf("expected data in response")
	}
}

func TestCreateBudget_ValidatesInput(t *testing.T) {
	fs := &fakeStore{}
	handler, err := NewAPIHandler(config.Config{}, fs, passkey.NewChallengeStore())
	if err != nil {
		t.Fatalf("NewAPIHandler error: %v", err)
	}

	body := bytes.NewBufferString(`{"name":"","payroll":-1}`)
	req := httptest.NewRequest(http.MethodPost, "/budgets", body)
	w := httptest.NewRecorder()
	handler.Router().ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}
