package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// Store wraps database access.
type Store struct {
	db *sql.DB
}

func New(db *sql.DB) *Store {
	return &Store{db: db}
}

type Budget struct {
	ID                 int64      `json:"id"`
	Name               string     `json:"name"`
	Payroll            float64    `json:"payroll"`
	PayrollRunAt       *time.Time `json:"payroll_run_at,omitempty"`
	AutoBalanceEnabled bool       `json:"auto_balance_enabled"`
	Credits            float64    `json:"credits"`
	Debits             float64    `json:"debits"`
	Balance            float64    `json:"balance"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
}

type Transaction struct {
	ID          int64     `json:"id"`
	BudgetID    int64     `json:"budget_id"`
	UserID      *int64    `json:"user_id,omitempty"`
	Description string    `json:"description"`
	Credit      bool      `json:"credit"`
	Amount      float64   `json:"amount"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type AutoBalanceSource struct {
	SourceBudgetID int64 `json:"source_budget_id"`
	Weight         int   `json:"weight"`
}

type User struct {
	ID                int64  `json:"id"`
	Email             string `json:"email"`
	EncryptedPassword string `json:"encrypted_password"`
}

type Passkey struct {
	ID             int64     `json:"id"`
	UserID         int64     `json:"user_id"`
	CredentialID   string    `json:"credential_id"`
	PublicKey      string    `json:"public_key"`
	SignCount      int       `json:"sign_count"`
	BackupEligible bool      `json:"backup_eligible"`
	BackupState    bool      `json:"backup_state"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type APIKey struct {
	ID         int64      `json:"id"`
	UserID     int64      `json:"user_id"`
	Email      string     `json:"email"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
}

var ErrNotFound = errors.New("not found")

func (s *Store) ListBudgets(ctx context.Context, userID *int64) ([]Budget, error) {
	base := `
		SELECT b.id, b.name, b.payroll, b.payroll_run_at, b.auto_balance_enabled, b.created_at, b.updated_at,
			COALESCE(SUM(CASE WHEN t.credit THEN t.amount ELSE 0 END), 0) AS credits,
			COALESCE(SUM(CASE WHEN t.credit THEN 0 ELSE t.amount END), 0) AS debits
		FROM budgets b
	`
	var args []any
	var where string
	if userID != nil {
		base += "JOIN users_budgets ub ON ub.budget_id = b.id "
		where = "WHERE ub.user_id = $1"
		args = append(args, *userID)
	}
	base += "LEFT JOIN transacts t ON t.budget_id = b.id "
	query := base + where + " GROUP BY b.id ORDER BY b.id;"

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var budgets []Budget
	for rows.Next() {
		var b Budget
		if err := rows.Scan(&b.ID, &b.Name, &b.Payroll, &b.PayrollRunAt, &b.AutoBalanceEnabled, &b.CreatedAt, &b.UpdatedAt, &b.Credits, &b.Debits); err != nil {
			return nil, err
		}
		b.Balance = b.Credits - b.Debits
		budgets = append(budgets, b)
	}
	return budgets, rows.Err()
}

func (s *Store) GetBudget(ctx context.Context, id int64, userID *int64) (Budget, error) {
	query := `
		SELECT b.id, b.name, b.payroll, b.payroll_run_at, b.auto_balance_enabled, b.created_at, b.updated_at,
			COALESCE(SUM(CASE WHEN t.credit THEN t.amount ELSE 0 END), 0) AS credits,
			COALESCE(SUM(CASE WHEN t.credit THEN 0 ELSE t.amount END), 0) AS debits
		FROM budgets b
	`
	var args []any
	var where = "WHERE b.id = $1"
	args = append(args, id)
	if userID != nil {
		query += "JOIN users_budgets ub ON ub.budget_id = b.id "
		where += " AND ub.user_id = $2"
		args = append(args, *userID)
	}
	query += "LEFT JOIN transacts t ON t.budget_id = b.id " + where + " GROUP BY b.id;"

	var b Budget
	err := s.db.QueryRowContext(ctx, query, args...).Scan(&b.ID, &b.Name, &b.Payroll, &b.PayrollRunAt, &b.AutoBalanceEnabled, &b.CreatedAt, &b.UpdatedAt, &b.Credits, &b.Debits)
	if errors.Is(err, sql.ErrNoRows) {
		return Budget{}, ErrNotFound
	}
	if err != nil {
		return Budget{}, err
	}
	b.Balance = b.Credits - b.Debits
	return b, nil
}

func (s *Store) CreateBudget(ctx context.Context, userID *int64, name string, payroll float64) (Budget, error) {
	const q = `
		INSERT INTO budgets (name, payroll, payroll_run_at, auto_balance_enabled, created_at, updated_at)
		VALUES ($1, $2, NULL, FALSE, NOW(), NOW())
		RETURNING id, name, payroll, payroll_run_at, auto_balance_enabled, created_at, updated_at;
	`
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Budget{}, err
	}
	defer tx.Rollback()

	var b Budget
	if err := tx.QueryRowContext(ctx, q, name, payroll).Scan(&b.ID, &b.Name, &b.Payroll, &b.PayrollRunAt, &b.AutoBalanceEnabled, &b.CreatedAt, &b.UpdatedAt); err != nil {
		return Budget{}, err
	}
	if userID != nil {
		if _, err := tx.ExecContext(ctx, `INSERT INTO users_budgets (user_id, budget_id) VALUES ($1, $2)`, *userID, b.ID); err != nil {
			return Budget{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Budget{}, err
	}
	return b, nil
}

func (s *Store) UpdateBudget(ctx context.Context, id int64, userID *int64, name string, payroll float64) (Budget, error) {
	if err := s.ensureBudgetAccess(ctx, id, userID); err != nil {
		return Budget{}, err
	}

	const q = `
		UPDATE budgets
		SET name = $1, payroll = $2, updated_at = NOW()
		WHERE id = $3
		RETURNING id, name, payroll, payroll_run_at, auto_balance_enabled, created_at, updated_at;
	`
	var b Budget
	err := s.db.QueryRowContext(ctx, q, name, payroll, id).Scan(&b.ID, &b.Name, &b.Payroll, &b.PayrollRunAt, &b.AutoBalanceEnabled, &b.CreatedAt, &b.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Budget{}, ErrNotFound
	}
	if err != nil {
		return Budget{}, err
	}
	return b, nil
}

func (s *Store) GetAutoBalanceConfig(ctx context.Context, budgetID int64, userID *int64) (bool, []AutoBalanceSource, error) {
	if err := s.ensureBudgetAccess(ctx, budgetID, userID); err != nil {
		return false, nil, err
	}
	var enabled bool
	if err := s.db.QueryRowContext(ctx, `SELECT auto_balance_enabled FROM budgets WHERE id = $1`, budgetID).Scan(&enabled); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil, ErrNotFound
		}
		return false, nil, err
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT source_budget_id, weight
		FROM budget_auto_balance_sources
		WHERE budget_id = $1
		ORDER BY source_budget_id;
	`, budgetID)
	if err != nil {
		return false, nil, err
	}
	defer rows.Close()
	var sources []AutoBalanceSource
	for rows.Next() {
		var s AutoBalanceSource
		if err := rows.Scan(&s.SourceBudgetID, &s.Weight); err != nil {
			return false, nil, err
		}
		sources = append(sources, s)
	}
	if err := rows.Err(); err != nil {
		return false, nil, err
	}
	return enabled, sources, nil
}

func (s *Store) UpdateAutoBalanceConfig(ctx context.Context, budgetID int64, userID *int64, enabled bool, sources []AutoBalanceSource) error {
	if err := s.ensureBudgetAccess(ctx, budgetID, userID); err != nil {
		return err
	}

	seen := make(map[int64]struct{}, len(sources))
	for _, source := range sources {
		if source.SourceBudgetID == budgetID {
			return fmt.Errorf("source budget cannot match target")
		}
		if source.Weight < 0 || source.Weight > 100 {
			return fmt.Errorf("weight must be between 0 and 100")
		}
		if _, ok := seen[source.SourceBudgetID]; ok {
			return fmt.Errorf("duplicate source budget")
		}
		seen[source.SourceBudgetID] = struct{}{}
		if err := s.ensureBudgetAccess(ctx, source.SourceBudgetID, userID); err != nil {
			return err
		}
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx, `
		UPDATE budgets
		SET auto_balance_enabled = $1, updated_at = NOW()
		WHERE id = $2
	`, enabled, budgetID)
	if err != nil {
		return err
	}
	if rows, _ := res.RowsAffected(); rows == 0 {
		return ErrNotFound
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM budget_auto_balance_sources WHERE budget_id = $1`, budgetID); err != nil {
		return err
	}

	for _, source := range sources {
		if source.Weight <= 0 {
			continue
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO budget_auto_balance_sources (budget_id, source_budget_id, weight)
			VALUES ($1, $2, $3)
		`, budgetID, source.SourceBudgetID, source.Weight); err != nil {
			return err
		}
	}

	return tx.Commit()
}

func (s *Store) DeleteBudget(ctx context.Context, id int64, userID *int64) error {
	if err := s.ensureBudgetAccess(ctx, id, userID); err != nil {
		return err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `DELETE FROM transacts WHERE budget_id = $1`, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM budget_auto_balance_sources WHERE budget_id = $1 OR source_budget_id = $1`, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM users_budgets WHERE budget_id = $1`, id); err != nil {
		return err
	}

	res, err := tx.ExecContext(ctx, `DELETE FROM budgets WHERE id = $1`, id)
	if err != nil {
		return err
	}
	count, _ := res.RowsAffected()
	if count == 0 {
		return ErrNotFound
	}
	return tx.Commit()
}

func (s *Store) ListTransactions(ctx context.Context, budgetID int64, userID *int64, limit int) ([]Transaction, error) {
	if err := s.ensureBudgetAccess(ctx, budgetID, userID); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	const q = `
		SELECT id, budget_id, user_id, description, credit, amount, created_at, updated_at
		FROM transacts
		WHERE budget_id = $1
		ORDER BY created_at DESC
		LIMIT $2;
	`
	rows, err := s.db.QueryContext(ctx, q, budgetID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var txns []Transaction
	for rows.Next() {
		var t Transaction
		if err := rows.Scan(&t.ID, &t.BudgetID, &t.UserID, &t.Description, &t.Credit, &t.Amount, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, err
		}
		txns = append(txns, t)
	}
	return txns, rows.Err()
}

func (s *Store) ListTransactionsPaged(ctx context.Context, budgetID int64, userID *int64, limit, offset int, search string) ([]Transaction, error) {
	if err := s.ensureBudgetAccess(ctx, budgetID, userID); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}

	var args []any
	args = append(args, budgetID)
	where := "budget_id = $1"
	if search != "" {
		args = append(args, "%"+search+"%")
		where += fmt.Sprintf(" AND (CAST(amount AS TEXT) ILIKE $%d OR description ILIKE $%d)", len(args), len(args))
	}
	args = append(args, limit, offset)

	query := fmt.Sprintf(`
		SELECT id, budget_id, user_id, description, credit, amount, created_at, updated_at
		FROM transacts
		WHERE %s
		ORDER BY created_at DESC
		LIMIT $%d OFFSET $%d;
	`, where, len(args)-1, len(args))

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var txns []Transaction
	for rows.Next() {
		var t Transaction
		if err := rows.Scan(&t.ID, &t.BudgetID, &t.UserID, &t.Description, &t.Credit, &t.Amount, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, err
		}
		txns = append(txns, t)
	}
	return txns, rows.Err()
}

func (s *Store) CreateTransaction(ctx context.Context, budgetID int64, userID *int64, description string, credit bool, amount float64) (Transaction, error) {
	if amount <= 0 {
		return Transaction{}, fmt.Errorf("amount must be > 0")
	}

	if err := s.ensureBudgetAccess(ctx, budgetID, userID); err != nil {
		return Transaction{}, err
	}

	const q = `
		INSERT INTO transacts (budget_id, user_id, description, credit, amount, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, NOW(), NOW())
		RETURNING id, budget_id, user_id, description, credit, amount, created_at, updated_at;
	`
	var t Transaction
	err := s.db.QueryRowContext(ctx, q, budgetID, userID, description, credit, amount).Scan(
		&t.ID, &t.BudgetID, &t.UserID, &t.Description, &t.Credit, &t.Amount, &t.CreatedAt, &t.UpdatedAt,
	)
	if err != nil {
		if isForeignKeyError(err) {
			return Transaction{}, ErrNotFound
		}
		return Transaction{}, err
	}
	return t, nil
}

func (s *Store) UpdateTransaction(ctx context.Context, budgetID, transactionID int64, userID *int64, description string, credit bool, amount float64) (Transaction, error) {
	if amount <= 0 {
		return Transaction{}, fmt.Errorf("amount must be > 0")
	}
	if err := s.ensureBudgetAccess(ctx, budgetID, userID); err != nil {
		return Transaction{}, err
	}

	const q = `
		UPDATE transacts
		SET description = $1, credit = $2, amount = $3, updated_at = NOW()
		WHERE id = $4 AND budget_id = $5
		RETURNING id, budget_id, user_id, description, credit, amount, created_at, updated_at;
	`
	var t Transaction
	err := s.db.QueryRowContext(ctx, q, description, credit, amount, transactionID, budgetID).Scan(
		&t.ID, &t.BudgetID, &t.UserID, &t.Description, &t.Credit, &t.Amount, &t.CreatedAt, &t.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Transaction{}, ErrNotFound
	}
	return t, err
}

func (s *Store) DeleteTransaction(ctx context.Context, budgetID, transactionID int64, userID *int64) error {
	if err := s.ensureBudgetAccess(ctx, budgetID, userID); err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM transacts WHERE id = $1 AND budget_id = $2`, transactionID, budgetID)
	if err != nil {
		return err
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) BudgetBalance(ctx context.Context, budgetID int64, userID *int64) (float64, error) {
	if err := s.ensureBudgetAccess(ctx, budgetID, userID); err != nil {
		return 0, err
	}

	const q = `
		SELECT COALESCE(SUM(CASE WHEN credit THEN amount ELSE -amount END), 0)
		FROM transacts
		WHERE budget_id = $1;
	`
	var balance float64
	err := s.db.QueryRowContext(ctx, q, budgetID).Scan(&balance)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	return balance, err
}

func (s *Store) GetUserByEmail(ctx context.Context, email string) (User, error) {
	const q = `
		SELECT id, email, encrypted_password
		FROM users
		WHERE email = $1;
	`
	var u User
	err := s.db.QueryRowContext(ctx, q, email).Scan(&u.ID, &u.Email, &u.EncryptedPassword)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	return u, err
}

func (s *Store) GetUserByID(ctx context.Context, id int64) (User, error) {
	const q = `
		SELECT id, email, encrypted_password
		FROM users
		WHERE id = $1;
	`
	var u User
	err := s.db.QueryRowContext(ctx, q, id).Scan(&u.ID, &u.Email, &u.EncryptedPassword)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	return u, err
}

func (s *Store) GetOrCreateUser(ctx context.Context, email string) (User, error) {
	const q = `
		INSERT INTO users (email, encrypted_password, created_at, updated_at)
		VALUES ($1, '', NOW(), NOW())
		ON CONFLICT (email) DO UPDATE SET updated_at = NOW()
		RETURNING id, email, encrypted_password;
	`
	var u User
	err := s.db.QueryRowContext(ctx, q, email).Scan(&u.ID, &u.Email, &u.EncryptedPassword)
	return u, err
}

func (s *Store) ListBudgetShares(ctx context.Context, budgetID int64, userID *int64) ([]User, error) {
	if err := s.ensureBudgetAccess(ctx, budgetID, userID); err != nil {
		return nil, err
	}
	const q = `
		SELECT u.id, u.email, u.encrypted_password
		FROM users u
		JOIN users_budgets ub ON ub.user_id = u.id
		WHERE ub.budget_id = $1
		ORDER BY u.email;
	`
	rows, err := s.db.QueryContext(ctx, q, budgetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var users []User
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Email, &u.EncryptedPassword); err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	return users, rows.Err()
}

func (s *Store) AddBudgetShare(ctx context.Context, budgetID int64, ownerID *int64, email string) (User, error) {
	if err := s.ensureBudgetAccess(ctx, budgetID, ownerID); err != nil {
		return User{}, err
	}
	user, err := s.GetOrCreateUser(ctx, email)
	if err != nil {
		return User{}, err
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO users_budgets (user_id, budget_id)
		VALUES ($1, $2)
		ON CONFLICT DO NOTHING;
	`, user.ID, budgetID)
	return user, err
}

func (s *Store) RemoveBudgetShare(ctx context.Context, budgetID int64, ownerID *int64, email string) error {
	if err := s.ensureBudgetAccess(ctx, budgetID, ownerID); err != nil {
		return err
	}
	var userID int64
	err := s.db.QueryRowContext(ctx, `SELECT id FROM users WHERE email = $1`, email).Scan(&userID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `DELETE FROM users_budgets WHERE user_id = $1 AND budget_id = $2`, userID, budgetID)
	return err
}

func (s *Store) ListAPIKeys(ctx context.Context, userID int64) ([]APIKey, error) {
	const q = `
		SELECT ak.id, ak.user_id, u.email, ak.name, ak.token_prefix, ak.created_at, ak.last_used_at
		FROM api_keys ak
		JOIN users u ON u.id = ak.user_id
		WHERE ak.user_id = $1
		ORDER BY ak.created_at DESC;
	`
	rows, err := s.db.QueryContext(ctx, q, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var keys []APIKey
	for rows.Next() {
		var key APIKey
		if err := rows.Scan(&key.ID, &key.UserID, &key.Email, &key.Name, &key.Prefix, &key.CreatedAt, &key.LastUsedAt); err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	return keys, rows.Err()
}

func (s *Store) CreateAPIKey(ctx context.Context, userID int64, name string) (APIKey, string, error) {
	token, prefix, hash, err := generateAPIKey()
	if err != nil {
		return APIKey{}, "", err
	}
	const q = `
		INSERT INTO api_keys (user_id, name, token_hash, token_prefix, created_at)
		VALUES ($1, $2, $3, $4, NOW())
		RETURNING id, created_at, last_used_at;
	`
	var key APIKey
	key.UserID = userID
	key.Name = name
	key.Prefix = prefix
	if err := s.db.QueryRowContext(ctx, q, userID, name, hash, prefix).Scan(&key.ID, &key.CreatedAt, &key.LastUsedAt); err != nil {
		return APIKey{}, "", err
	}
	user, err := s.GetUserByID(ctx, userID)
	if err == nil {
		key.Email = user.Email
	}
	return key, token, nil
}

func (s *Store) DeleteAPIKey(ctx context.Context, userID, keyID int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM api_keys WHERE id = $1 AND user_id = $2`, keyID, userID)
	if err != nil {
		return err
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) GetAPIKeyByToken(ctx context.Context, token string) (APIKey, error) {
	hash := hashAPIKey(token)
	const q = `
		SELECT ak.id, ak.user_id, u.email, ak.name, ak.token_prefix, ak.created_at, ak.last_used_at
		FROM api_keys ak
		JOIN users u ON u.id = ak.user_id
		WHERE ak.token_hash = $1;
	`
	var key APIKey
	if err := s.db.QueryRowContext(ctx, q, hash).Scan(&key.ID, &key.UserID, &key.Email, &key.Name, &key.Prefix, &key.CreatedAt, &key.LastUsedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return APIKey{}, ErrNotFound
		}
		return APIKey{}, err
	}
	_, _ = s.db.ExecContext(ctx, `UPDATE api_keys SET last_used_at = NOW() WHERE id = $1`, key.ID)
	return key, nil
}

func (s *Store) GetPasskeyByUser(ctx context.Context, userID int64) (Passkey, error) {
	const q = `
		SELECT id, user_id, credential_id, public_key, sign_count, backup_eligible, backup_state, created_at, updated_at
		FROM passkeys
		WHERE user_id = $1;
	`
	var p Passkey
	err := s.db.QueryRowContext(ctx, q, userID).Scan(
		&p.ID, &p.UserID, &p.CredentialID, &p.PublicKey, &p.SignCount, &p.BackupEligible, &p.BackupState, &p.CreatedAt, &p.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Passkey{}, ErrNotFound
	}
	return p, err
}

func (s *Store) GetPasskeyByCredentialID(ctx context.Context, credentialID string) (Passkey, error) {
	const q = `
		SELECT id, user_id, credential_id, public_key, sign_count, backup_eligible, backup_state, created_at, updated_at
		FROM passkeys
		WHERE credential_id = $1;
	`
	var p Passkey
	err := s.db.QueryRowContext(ctx, q, credentialID).Scan(
		&p.ID, &p.UserID, &p.CredentialID, &p.PublicKey, &p.SignCount, &p.BackupEligible, &p.BackupState, &p.CreatedAt, &p.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Passkey{}, ErrNotFound
	}
	return p, err
}

func (s *Store) CreatePasskey(ctx context.Context, userID int64, credentialID, publicKey string, signCount int, backupEligible, backupState bool) (Passkey, error) {
	if credentialID == "" {
		return Passkey{}, fmt.Errorf("credential_id required")
	}
	const q = `
		INSERT INTO passkeys (user_id, credential_id, public_key, sign_count, backup_eligible, backup_state, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, NOW(), NOW())
		RETURNING id, user_id, credential_id, public_key, sign_count, backup_eligible, backup_state, created_at, updated_at;
	`
	var p Passkey
	err := s.db.QueryRowContext(ctx, q, userID, credentialID, publicKey, signCount, backupEligible, backupState).Scan(
		&p.ID, &p.UserID, &p.CredentialID, &p.PublicKey, &p.SignCount, &p.BackupEligible, &p.BackupState, &p.CreatedAt, &p.UpdatedAt,
	)
	if err != nil {
		if isUniqueViolation(err) || isForeignKeyError(err) {
			return Passkey{}, ErrNotFound
		}
		return Passkey{}, err
	}
	return p, nil
}

func generateAPIKey() (string, string, string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", "", err
	}
	encoded := base64.RawURLEncoding.EncodeToString(raw)
	token := "mpb_" + encoded
	prefix := token
	if len(encoded) >= 8 {
		prefix = "mpb_" + encoded[:8]
	}
	return token, prefix, hashAPIKey(token), nil
}

func hashAPIKey(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func (s *Store) UpdatePasskeySignCount(ctx context.Context, credentialID string, signCount int) error {
	const q = `
		UPDATE passkeys
		SET sign_count = $1, updated_at = NOW()
		WHERE credential_id = $2
	`
	_, err := s.db.ExecContext(ctx, q, signCount, credentialID)
	return err
}

func (s *Store) ensureBudgetAccess(ctx context.Context, budgetID int64, userID *int64) error {
	if userID == nil {
		return nil
	}
	var exists bool
	err := s.db.QueryRowContext(ctx, `SELECT TRUE FROM users_budgets WHERE budget_id = $1 AND user_id = $2`, budgetID, *userID).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

func isForeignKeyError(err error) bool {
	// pgx returns errors containing "foreign key violation"
	return err != nil && strings.Contains(err.Error(), "foreign key violation")
}

func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "unique constraint")
}

// Ping verifies database connectivity.
func (s *Store) Ping(ctx context.Context) error {
	return s.db.PingContext(ctx)
}

// RunMonthlyPayroll inserts a payroll credit transaction for each budget that
// hasn't been processed in the current month. It updates payroll_run_at to
// prevent duplicate credits within the same month. Returns the number of
// transactions created.
func (s *Store) RunMonthlyPayroll(ctx context.Context, now time.Time) (int, error) {
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	rows, err := tx.QueryContext(ctx, `
		SELECT id, name, payroll, auto_balance_enabled, payroll_run_at
		FROM budgets
		WHERE payroll > 0
			AND (payroll_run_at IS NULL OR payroll_run_at < $1)
		FOR UPDATE;
	`, monthStart)
	if err != nil {
		return 0, fmt.Errorf("select budgets: %w", err)
	}
	defer rows.Close()

	var pending []payrollBudget
	created := 0
	for rows.Next() {
		var pb payrollBudget
		if err := rows.Scan(&pb.id, &pb.name, &pb.payroll, &pb.autoBalanceEnabled, &pb.payrollRunAt); err != nil {
			return 0, fmt.Errorf("scan budget: %w", err)
		}
		pending = append(pending, pb)
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("rows err: %w", err)
	}

	for _, pb := range pending {
		if err := runPayrollForBudgetTx(ctx, tx, pb, now, monthStart, false); err != nil {
			return 0, err
		}
		created++
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit: %w", err)
	}
	return created, nil
}

func payrollDescription(now time.Time) string {
	return fmt.Sprintf("Payroll %s", now.Format("January 2006"))
}

func (s *Store) RunBudgetPayroll(ctx context.Context, budgetID int64, userID *int64, now time.Time, force bool) (int, error) {
	if err := s.ensureBudgetAccess(ctx, budgetID, userID); err != nil {
		return 0, err
	}
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	var pb payrollBudget
	if err := tx.QueryRowContext(ctx, `
		SELECT id, name, payroll, auto_balance_enabled, payroll_run_at
		FROM budgets
		WHERE id = $1
		FOR UPDATE;
	`, budgetID).Scan(&pb.id, &pb.name, &pb.payroll, &pb.autoBalanceEnabled, &pb.payrollRunAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, ErrNotFound
		}
		return 0, fmt.Errorf("select budget: %w", err)
	}
	if pb.payroll <= 0 {
		return 0, nil
	}
	if !force && pb.payrollRunAt != nil && !pb.payrollRunAt.Before(monthStart) {
		return 0, nil
	}

	if err := runPayrollForBudgetTx(ctx, tx, pb, now, monthStart, force); err != nil {
		return 0, err
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit: %w", err)
	}
	return 1, nil
}

type payrollBudget struct {
	id                 int64
	name               string
	payroll            float64
	autoBalanceEnabled bool
	payrollRunAt       *time.Time
}

func runPayrollForBudgetTx(
	ctx context.Context,
	tx *sql.Tx,
	pb payrollBudget,
	now time.Time,
	monthStart time.Time,
	force bool,
) error {
	if pb.payroll <= 0 {
		return nil
	}
	if !force && pb.payrollRunAt != nil && !pb.payrollRunAt.Before(monthStart) {
		return nil
	}
	if pb.autoBalanceEnabled {
		if err := applyAutoBalanceTx(ctx, tx, pb.id, pb.name); err != nil {
			return fmt.Errorf("auto-balance budget %d: %w", pb.id, err)
		}
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO transacts (budget_id, user_id, description, credit, amount, created_at, updated_at)
		VALUES ($1, NULL, $2, TRUE, $3, NOW(), NOW())
	`, pb.id, payrollDescription(now), pb.payroll); err != nil {
		return fmt.Errorf("insert payroll txn: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE budgets
		SET payroll_run_at = NOW(), updated_at = NOW()
		WHERE id = $1
	`, pb.id); err != nil {
		return fmt.Errorf("update payroll_run_at: %w", err)
	}
	return nil
}

func applyAutoBalanceTx(ctx context.Context, tx *sql.Tx, budgetID int64, budgetName string) error {
	var balance float64
	if err := tx.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(CASE WHEN credit THEN amount ELSE -amount END), 0)
		FROM transacts
		WHERE budget_id = $1;
	`, budgetID).Scan(&balance); err != nil {
		return fmt.Errorf("select balance: %w", err)
	}
	if balance >= 0 {
		return nil
	}

	deficitCents := int64(math.Round(math.Abs(balance) * 100))
	if deficitCents <= 0 {
		return nil
	}

	rows, err := tx.QueryContext(ctx, `
		SELECT source_budget_id, weight
		FROM budget_auto_balance_sources
		WHERE budget_id = $1
		ORDER BY source_budget_id;
	`, budgetID)
	if err != nil {
		return fmt.Errorf("select sources: %w", err)
	}
	defer rows.Close()

	var sources []AutoBalanceSource
	for rows.Next() {
		var source AutoBalanceSource
		if err := rows.Scan(&source.SourceBudgetID, &source.Weight); err != nil {
			return fmt.Errorf("scan source: %w", err)
		}
		sources = append(sources, source)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("sources err: %w", err)
	}
	if len(sources) == 0 {
		return nil
	}

	allocations := allocateWeightedCents(deficitCents, sources)
	var totalAllocated int64
	description := fmt.Sprintf("Auto-balance for %s", budgetName)
	for i, source := range sources {
		if allocations[i] <= 0 {
			continue
		}
		amount := float64(allocations[i]) / 100
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO transacts (budget_id, user_id, description, credit, amount, created_at, updated_at)
			VALUES ($1, NULL, $2, FALSE, $3, NOW(), NOW())
		`, source.SourceBudgetID, description, amount); err != nil {
			return fmt.Errorf("insert source debit: %w", err)
		}
		totalAllocated += allocations[i]
	}

	if totalAllocated <= 0 {
		return nil
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO transacts (budget_id, user_id, description, credit, amount, created_at, updated_at)
		VALUES ($1, NULL, $2, TRUE, $3, NOW(), NOW())
	`, budgetID, description, float64(totalAllocated)/100); err != nil {
		return fmt.Errorf("insert target credit: %w", err)
	}
	return nil
}

func allocateWeightedCents(totalCents int64, sources []AutoBalanceSource) []int64 {
	allocations := make([]int64, len(sources))
	if totalCents <= 0 {
		return allocations
	}
	totalWeight := 0
	for _, source := range sources {
		if source.Weight > 0 {
			totalWeight += source.Weight
		}
	}
	if totalWeight <= 0 {
		return allocations
	}

	type remainderItem struct {
		idx       int
		remainder float64
	}

	var allocated int64
	remainders := make([]remainderItem, 0, len(sources))
	for i, source := range sources {
		if source.Weight <= 0 {
			continue
		}
		exact := float64(totalCents) * float64(source.Weight) / float64(totalWeight)
		base := int64(math.Floor(exact))
		allocations[i] = base
		allocated += base
		remainders = append(remainders, remainderItem{idx: i, remainder: exact - float64(base)})
	}

	remaining := totalCents - allocated
	sort.Slice(remainders, func(i, j int) bool {
		return remainders[i].remainder > remainders[j].remainder
	})
	for remaining > 0 && len(remainders) > 0 {
		for _, item := range remainders {
			if remaining == 0 {
				break
			}
			allocations[item.idx]++
			remaining--
		}
	}
	return allocations
}
