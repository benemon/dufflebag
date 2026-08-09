package postgres

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"

	"github.com/golang-migrate/migrate/v4"
	migratepostgres "github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Migrate applies all known schema migrations.
func Migrate(db *sql.DB) error {
	source, err := iofs.New(migrations, "migrations")
	if err != nil {
		return fmt.Errorf("open migrations: %w", err)
	}
	conn, err := db.Conn(context.Background())
	if err != nil {
		return fmt.Errorf("open postgres migration connection: %w", err)
	}
	driver, err := migratepostgres.WithConnection(context.Background(), conn, &migratepostgres.Config{})
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("open postgres migration driver: %w", err)
	}
	m, err := migrate.NewWithInstance("iofs", source, "postgres", driver)
	if err != nil {
		_ = driver.Close()
		return fmt.Errorf("create migrator: %w", err)
	}
	upErr := m.Up()
	sourceErr, databaseErr := m.Close()
	if upErr != nil && !errors.Is(upErr, migrate.ErrNoChange) {
		return fmt.Errorf("apply migrations: %w", upErr)
	}
	if err := errors.Join(sourceErr, databaseErr); err != nil {
		return fmt.Errorf("close migrator: %w", err)
	}
	return nil
}
