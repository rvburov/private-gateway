package storage

import (
	"context"
	"crypto/sha256"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// RememberAdministratorLogin сохраняет только хеш ключа браузера и ограничивает число записей.
func (s *Store) RememberAdministratorLogin(ctx context.Context, token string, generation int64, expires time.Time) error {
	hash := sha256.Sum256([]byte(token))
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(ctx, `DELETE FROM administrator_remembered_logins WHERE expires_at <= now()`); err != nil {
		return err
	}
	result, err := tx.Exec(ctx, `INSERT INTO administrator_remembered_logins (token_hash, administrator_id, generation, expires_at)
		SELECT $1, id, generation, $3 FROM administrator_credentials
		WHERE id = 1 AND generation = $2 AND totp_secret_ciphertext IS NOT NULL`, hash[:], generation, expires)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return ErrNotFound
	}
	if _, err = tx.Exec(ctx, `DELETE FROM administrator_remembered_logins WHERE token_hash IN (
		SELECT token_hash FROM administrator_remembered_logins ORDER BY expires_at DESC OFFSET 32
	)`); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// AdministratorRememberedLogin проверяет срок и поколение учётной записи.
func (s *Store) AdministratorRememberedLogin(ctx context.Context, token string) (int64, bool, error) {
	hash := sha256.Sum256([]byte(token))
	var generation int64
	err := s.pool.QueryRow(ctx, `SELECT l.generation FROM administrator_remembered_logins l
		JOIN administrator_credentials a ON a.id = l.administrator_id
		WHERE l.token_hash = $1 AND l.expires_at > now() AND l.generation = a.generation
		AND a.totp_secret_ciphertext IS NOT NULL
`, hash[:]).Scan(&generation)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	return generation, err == nil, err
}

// ForgetAdministratorLogin отзывает ключ только из этого браузера.
func (s *Store) ForgetAdministratorLogin(ctx context.Context, token string) error {
	hash := sha256.Sum256([]byte(token))
	_, err := s.pool.Exec(ctx, `DELETE FROM administrator_remembered_logins WHERE token_hash = $1`, hash[:])
	return err
}
