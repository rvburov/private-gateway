package storage

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/rvburov/private-gateway/internal/adminauth"
	"github.com/rvburov/private-gateway/internal/storage/db"
)

const administratorTOTPRecord = "administrator-totp-1"

type Administrator struct {
	Credentials adminauth.Credentials
	Generation  int64
	LastStep    int64
}

// AdministratorSnapshot — запись для отката, доступная только root. Её хеш пароля и
// зашифрованный секрет TOTP никогда не должны попадать в ответы HTTP и журналы.
type AdministratorSnapshot struct {
	Exists               bool   `json:"exists"`
	Username             string `json:"username,omitempty"`
	PasswordHash         string `json:"password_hash,omitempty"`
	TOTPSecretCiphertext []byte `json:"totp_secret_ciphertext,omitempty"`
	TOTPLastStep         *int64 `json:"totp_last_step,omitempty"`
	Generation           int64  `json:"generation,omitempty"`
}

func (s *Store) SnapshotAdministrator(ctx context.Context) (AdministratorSnapshot, error) {
	row, err := s.queries.GetAdministrator(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return AdministratorSnapshot{}, nil
	}
	if err != nil {
		return AdministratorSnapshot{}, err
	}
	result := AdministratorSnapshot{Exists: true, Username: row.Username,
		PasswordHash: row.PasswordHash, TOTPSecretCiphertext: row.TotpSecretCiphertext,
		Generation: row.Generation}
	if row.TotpLastStep.Valid {
		step := row.TotpLastStep.Int64
		result.TOTPLastStep = &step
	}
	return result, nil
}

func (s *Store) RestoreAdministrator(ctx context.Context, before AdministratorSnapshot) (bool, error) {
	expected := before.Generation + 1
	if !before.Exists {
		count, err := s.queries.DeleteAdministrator(ctx, expected)
		return count == 1, err
	}
	if err := (adminauth.Credentials{Username: before.Username, PasswordHash: before.PasswordHash}).ValidatePassword(); err != nil {
		return false, err
	}
	step := pgtype.Int8{}
	if before.TOTPLastStep != nil {
		step = pgtype.Int8{Int64: *before.TOTPLastStep, Valid: true}
	}
	count, err := s.queries.RestoreAdministrator(ctx, db.RestoreAdministratorParams{
		Username: before.Username, PasswordHash: before.PasswordHash,
		TotpSecretCiphertext: before.TOTPSecretCiphertext, TotpLastStep: step, Generation: expected,
	})
	return count == 1, err
}

func (s *Store) Administrator(ctx context.Context) (Administrator, error) {
	row, err := s.queries.GetAdministrator(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return Administrator{}, nil
	}
	if err != nil {
		return Administrator{}, err
	}
	result := Administrator{
		Credentials: adminauth.Credentials{Username: row.Username, PasswordHash: row.PasswordHash},
		Generation:  row.Generation, LastStep: -1,
	}
	if err := result.Credentials.ValidatePassword(); err != nil {
		return Administrator{}, err
	}
	if row.TotpSecretCiphertext != nil {
		result.Credentials.TOTPSecret, err = s.cipher.Decrypt(row.TotpSecretCiphertext, administratorTOTPRecord)
		if err != nil {
			return Administrator{}, err
		}
		result.LastStep = row.TotpLastStep.Int64
		if err := result.Credentials.Validate(); err != nil {
			return Administrator{}, err
		}
	}
	return result, nil
}

// ImportAdministrator при обновлении сохраняет прежние логин и пароль, но требует заново
// подключить аутентификатор в браузере. Прежний секрет TOTP никогда не переносится.
func (s *Store) ImportAdministrator(ctx context.Context, username, passwordHash string) error {
	if username == "" {
		return nil
	}
	if err := (adminauth.Credentials{Username: username, PasswordHash: passwordHash}).ValidatePassword(); err != nil {
		return err
	}
	return s.queries.ImportAdministrator(ctx, db.ImportAdministratorParams{Username: username, PasswordHash: passwordHash})
}

func (s *Store) SetAdministrator(ctx context.Context, username, passwordHash string) error {
	if err := (adminauth.Credentials{Username: username, PasswordHash: passwordHash}).ValidatePassword(); err != nil {
		return err
	}
	return s.queries.SetAdministrator(ctx, db.SetAdministratorParams{Username: username, PasswordHash: passwordHash})
}

func (s *Store) EnableAdministratorTOTP(ctx context.Context, secret string, step, generation int64) (bool, error) {
	ciphertext, err := s.cipher.Encrypt(secret, administratorTOTPRecord)
	if err != nil {
		return false, err
	}
	count, err := s.queries.EnableAdministratorTOTP(ctx, db.EnableAdministratorTOTPParams{
		TotpSecretCiphertext: ciphertext, TotpLastStep: pgtype.Int8{Int64: step, Valid: true}, Generation: generation,
	})
	return count == 1, err
}

func (s *Store) UseAdministratorTOTP(ctx context.Context, step, generation int64) (bool, error) {
	count, err := s.queries.UseAdministratorTOTP(ctx, db.UseAdministratorTOTPParams{
		TotpLastStep: pgtype.Int8{Int64: step, Valid: true}, Generation: generation,
	})
	return count == 1, err
}
