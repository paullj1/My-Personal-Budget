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

	"my-personal-budget/internal/auth"
	"my-personal-budget/internal/config"
	"my-personal-budget/internal/passkey"
	"my-personal-budget/internal/store"
)

type fakeStore struct {
	budgets       []store.Budget
	createdBudget *store.Budget
	updatedBudget *store.Budget
	deletedBudget *int64
	user          *store.User
	passkey       *store.Passkey
	apiKeys       []store.APIKey
	shares        []store.User
	sharesErr     error
	addShareErr   error
	removeErr     error
	payrollCount  int
	payrollErr    error
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
	for i, b := range f.budgets {
		if b.ID == id {
			f.budgets[i].Name = name
			f.budgets[i].Payroll = payroll
			updated := f.budgets[i]
			f.updatedBudget = &updated
			return updated, nil
		}
	}
	return store.Budget{}, store.ErrNotFound
}

func (f *fakeStore) DeleteBudget(ctx context.Context, id int64, userID *int64) error {
	for _, b := range f.budgets {
		if b.ID == id {
			f.deletedBudget = &id
			return nil
		}
	}
	return store.ErrNotFound
}

func (f *fakeStore) GetAutoBalanceConfig(ctx context.Context, budgetID int64, userID *int64) (bool, []store.AutoBalanceSource, error) {
	return false, nil, nil
}

func (f *fakeStore) UpdateAutoBalanceConfig(ctx context.Context, budgetID int64, userID *int64, enabled bool, sources []store.AutoBalanceSource) error {
	return nil
}

func (f *fakeStore) RunMonthlyPayroll(ctx context.Context, now time.Time) (int, error) {
	return f.payrollCount, f.payrollErr
}

func (f *fakeStore) RunBudgetPayroll(ctx context.Context, budgetID int64, userID *int64, now time.Time, force bool) (int, error) {
	return 0, nil
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

func (f *fakeStore) ListAPIKeys(ctx context.Context, userID int64) ([]store.APIKey, error) {
	return f.apiKeys, nil
}

func (f *fakeStore) CreateAPIKey(ctx context.Context, userID int64, name string) (store.APIKey, string, error) {
	key := store.APIKey{
		ID:        int64(len(f.apiKeys) + 1),
		UserID:    userID,
		Name:      name,
		Prefix:    "mpb_test",
		CreatedAt: time.Now(),
	}
	f.apiKeys = append(f.apiKeys, key)
	return key, "mpb_test_token", nil
}

func (f *fakeStore) DeleteAPIKey(ctx context.Context, userID, keyID int64) error {
	return nil
}

func (f *fakeStore) GetUserByEmail(ctx context.Context, email string) (store.User, error) {
	if f.user != nil && strings.EqualFold(f.user.Email, email) {
		return *f.user, nil
	}
	return store.User{}, store.ErrNotFound
}

func (f *fakeStore) GetUserByID(ctx context.Context, id int64) (store.User, error) {
	if f.user != nil && f.user.ID == id {
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
	if f.sharesErr != nil {
		return nil, f.sharesErr
	}
	return f.shares, nil
}

func (f *fakeStore) AddBudgetShare(ctx context.Context, budgetID int64, ownerID *int64, email string) (store.User, error) {
	if f.addShareErr != nil {
		return store.User{}, f.addShareErr
	}
	return store.User{ID: 2, Email: email}, nil
}

func (f *fakeStore) RemoveBudgetShare(ctx context.Context, budgetID int64, ownerID *int64, email string) error {
	if f.removeErr != nil {
		return f.removeErr
	}
	return nil
}

func (f *fakeStore) GetPasskeyByUser(ctx context.Context, userID int64) (store.Passkey, error) {
	if f.passkey != nil && f.passkey.UserID == userID {
		return *f.passkey, nil
	}
	return store.Passkey{}, store.ErrNotFound
}

func (f *fakeStore) GetPasskeyByCredentialID(ctx context.Context, credentialID string) (store.Passkey, error) {
	if f.passkey != nil && f.passkey.CredentialID == credentialID {
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

func TestCreateBudget_Succeeds(t *testing.T) {
	fs := &fakeStore{}
	handler, err := NewAPIHandler(config.Config{}, fs, passkey.NewChallengeStore())
	if err != nil {
		t.Fatalf("NewAPIHandler error: %v", err)
	}

	body := bytes.NewBufferString(`{"name":"Groceries","payroll":250}`)
	req := httptest.NewRequest(http.MethodPost, "/budgets", body)
	w := httptest.NewRecorder()
	handler.Router().ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", w.Code)
	}
	if fs.createdBudget == nil || fs.createdBudget.Name != "Groceries" || fs.createdBudget.Payroll != 250 {
		t.Fatalf("expected budget created, got %+v", fs.createdBudget)
	}
}

func TestHandleBudgetByID_InvalidID(t *testing.T) {
	fs := &fakeStore{}
	handler, err := NewAPIHandler(config.Config{}, fs, passkey.NewChallengeStore())
	if err != nil {
		t.Fatalf("NewAPIHandler error: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/budgets/not-a-number", nil)
	w := httptest.NewRecorder()
	handler.Router().ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestGetBudget_NotFound(t *testing.T) {
	fs := &fakeStore{}
	handler, err := NewAPIHandler(config.Config{}, fs, passkey.NewChallengeStore())
	if err != nil {
		t.Fatalf("NewAPIHandler error: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/budgets/42", nil)
	w := httptest.NewRecorder()
	handler.Router().ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestUpdateBudget_Succeeds(t *testing.T) {
	fs := &fakeStore{
		budgets: []store.Budget{
			{ID: 2, Name: "Rent", Payroll: 1000, CreatedAt: time.Now(), UpdatedAt: time.Now()},
		},
	}
	handler, err := NewAPIHandler(config.Config{}, fs, passkey.NewChallengeStore())
	if err != nil {
		t.Fatalf("NewAPIHandler error: %v", err)
	}

	body := bytes.NewBufferString(`{"name":"Rent Updated","payroll":1200}`)
	req := httptest.NewRequest(http.MethodPut, "/budgets/2", body)
	w := httptest.NewRecorder()
	handler.Router().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if fs.updatedBudget == nil || fs.updatedBudget.Name != "Rent Updated" || fs.updatedBudget.Payroll != 1200 {
		t.Fatalf("expected updated budget, got %+v", fs.updatedBudget)
	}
}

func TestSharesLifecycle(t *testing.T) {
	fs := &fakeStore{
		shares: []store.User{
			{ID: 2, Email: "friend@example.com"},
		},
	}
	handler, err := NewAPIHandler(config.Config{}, fs, passkey.NewChallengeStore())
	if err != nil {
		t.Fatalf("NewAPIHandler error: %v", err)
	}

	listReq := httptest.NewRequest(http.MethodGet, "/budgets/1/shares", nil)
	listW := httptest.NewRecorder()
	handler.Router().ServeHTTP(listW, listReq)

	if listW.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", listW.Code)
	}

	addReq := httptest.NewRequest(http.MethodPost, "/budgets/1/shares", bytes.NewBufferString(`{"email":"new@example.com"}`))
	addW := httptest.NewRecorder()
	handler.Router().ServeHTTP(addW, addReq)

	if addW.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", addW.Code)
	}

	removeReq := httptest.NewRequest(http.MethodDelete, "/budgets/1/shares", bytes.NewBufferString(`{"email":"new@example.com"}`))
	removeW := httptest.NewRecorder()
	handler.Router().ServeHTTP(removeW, removeReq)

	if removeW.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", removeW.Code)
	}
}

func TestHandlePayrollRun_RequiresAuth(t *testing.T) {
	fs := &fakeStore{}
	handler, err := NewAPIHandler(config.Config{JWTSecret: "secret"}, fs, passkey.NewChallengeStore())
	if err != nil {
		t.Fatalf("NewAPIHandler error: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/payroll/run", nil)
	w := httptest.NewRecorder()
	handler.Router().ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestHandlePayrollRun_SucceedsForUser(t *testing.T) {
	fs := &fakeStore{payrollCount: 3}
	handler, err := NewAPIHandler(config.Config{JWTSecret: "secret"}, fs, passkey.NewChallengeStore())
	if err != nil {
		t.Fatalf("NewAPIHandler error: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/payroll/run", nil)
	req = req.WithContext(auth.WithUserID(req.Context(), 7))
	w := httptest.NewRecorder()
	handler.Router().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp["count"] != float64(3) {
		t.Fatalf("expected count 3, got %v", resp["count"])
	}
}
