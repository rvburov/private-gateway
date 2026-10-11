package storage

import (
	"database/sql"
	"embed"
	"errors"

	"github.com/golang-migrate/migrate/v4"
	pgxmigrate "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	_ "github.com/jackc/pgx/v5/stdlib"
)

//go:embed migrations/*.sql
var migrations embed.FS

func Migrate(dsn string) error {
	database, err := sql.Open("pgx", dsn)
	if err != nil {
		return errors.New("invalid database configuration")
	}
	defer database.Close()
	source, err := iofs.New(migrations, "migrations")
	if err != nil {
		return err
	}
	driver, err := pgxmigrate.WithInstance(database, &pgxmigrate.Config{})
	if err != nil {
		return errors.New("cannot connect to PostgreSQL for migrations")
	}
	migration, err := migrate.NewWithInstance("iofs", source, "pgx5", driver)
	if err != nil {
		return err
	}
	defer migration.Close()
	if err = migration.Up(); errors.Is(err, migrate.ErrNoChange) {
		return nil
	}
	return err
}
