package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
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
	ID           int64      `json:"id"`
	Name         string     `json:"name"`
	Payroll      float64    `json:"payroll"`
	PayrollRunAt *time.Time `json:"payroll_run_at,omitempty"`
	Credits      float64    `json:"credits"`
	Debits       float64    `json:"debits"`
	Balance      float64    `json:"balance"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
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

var ErrNotFound = errors.New("not found")

func (s *Store) ListBudgets(ctx context.Context, userID *int64) ([]Budget, error) {
	base := `
		SELECT b.id, b.name, b.payroll, b.payroll_run_at, b.created_at, b.updated_at,
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
		if err := rows.Scan(&b.ID, &b.Name, &b.Payroll, &b.PayrollRunAt, &b.CreatedAt, &b.UpdatedAt, &b.Credits, &b.Debits); err != nil {
			return nil, err
		}
		b.Balance = b.Credits - b.Debits
		budgets = append(budgets, b)
	}
	return budgets, rows.Err()
}

func (s *Store) GetBudget(ctx context.Context, id int64, userID *int64) (Budget, error) {
	query := `
		SELECT b.id, b.name, b.payroll, b.payroll_run_at, b.created_at, b.updated_at,
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
	err := s.db.QueryRowContext(ctx, query, args...).Scan(&b.ID, &b.Name, &b.Payroll, &b.PayrollRunAt, &b.CreatedAt, &b.UpdatedAt, &b.Credits, &b.Debits)
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
		INSERT INTO budgets (name, payroll, payroll_run_at, created_at, updated_at)
		VALUES ($1, $2, NULL, NOW(), NOW())
		RETURNING id, name, payroll, payroll_run_at, created_at, updated_at;
	`
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Budget{}, err
	}
	defer tx.Rollback()

	var b Budget
	if err := tx.QueryRowContext(ctx, q, name, payroll).Scan(&b.ID, &b.Name, &b.Payroll, &b.PayrollRunAt, &b.CreatedAt, &b.UpdatedAt); err != nil {
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
		RETURNING id, name, payroll, payroll_run_at, created_at, updated_at;
	`
	var b Budget
	err := s.db.QueryRowContext(ctx, q, name, payroll, id).Scan(&b.ID, &b.Name, &b.Payroll, &b.PayrollRunAt, &b.CreatedAt, &b.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Budget{}, ErrNotFound
	}
	if err != nil {
		return Budget{}, err
	}
	return b, nil
}

func (s *Store) DeleteBudget(ctx context.Context, id int64, userID *int64) error {
	if err := s.ensureBudgetAccess(ctx, id, userID); err != nil {
		return err
	}

	res, err := s.db.ExecContext(ctx, `DELETE FROM budgets WHERE id = $1`, id)
	if err != nil {
		return err
	}
	count, _ := res.RowsAffected()
	if count == 0 {
		return ErrNotFound
	}
	return nil
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
		SELECT id, payroll
		FROM budgets
		WHERE payroll > 0
			AND (payroll_run_at IS NULL OR payroll_run_at < $1)
		FOR UPDATE;
	`, monthStart)
	if err != nil {
		return 0, fmt.Errorf("select budgets: %w", err)
	}
	defer rows.Close()

	type payrollBudget struct {
		id      int64
		payroll float64
	}
	var pending []payrollBudget
	created := 0
	for rows.Next() {
		var pb payrollBudget
		if err := rows.Scan(&pb.id, &pb.payroll); err != nil {
			return 0, fmt.Errorf("scan budget: %w", err)
		}
		pending = append(pending, pb)
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("rows err: %w", err)
	}

	for _, pb := range pending {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO transacts (budget_id, user_id, description, credit, amount, created_at, updated_at)
			VALUES ($1, NULL, $2, TRUE, $3, NOW(), NOW())
		`, pb.id, payrollDescription(now), pb.payroll); err != nil {
			return 0, fmt.Errorf("insert payroll txn: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE budgets
			SET payroll_run_at = NOW(), updated_at = NOW()
			WHERE id = $1
		`, pb.id); err != nil {
			return 0, fmt.Errorf("update payroll_run_at: %w", err)
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
