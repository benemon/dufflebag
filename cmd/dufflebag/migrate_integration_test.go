//go:build integration

package main

import (
	"context"
	"database/sql"
	"os"
	"os/exec"
	"testing"

	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

// The subcommand is the init-container contract: a privileged role applies the
// schema and the process exits zero, with no serving attempt. This drives the
// real binary the way that container would.
func TestMigrateSubcommandPreparesAFreshDatabase(t *testing.T) {
	ctx := context.Background()
	container, err := tcpostgres.Run(
		ctx,
		"postgres:17-alpine",
		tcpostgres.WithDatabase("dufflebag"),
		tcpostgres.WithUsername("postgres"),
		tcpostgres.WithPassword("postgres"),
		tcpostgres.BasicWaitStrategies(),
	)
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })
	adminURL, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("postgres connection string: %v", err)
	}

	migrate := func() ([]byte, error) {
		cmd := exec.Command(runtimeBinary, "migrate")
		cmd.Env = append(os.Environ(), "DFBG_DATABASE_URL="+adminURL)
		return cmd.CombinedOutput()
	}
	output, err := migrate()
	if err != nil {
		t.Fatalf("migrate subcommand failed: %v\n%s", err, output)
	}
	// Idempotence is what lets an init container run on every deploy.
	if output, err := migrate(); err != nil {
		t.Fatalf("second migrate run failed: %v\n%s", err, output)
	}

	db, err := sql.Open("pgx", adminURL)
	if err != nil {
		t.Fatalf("open migrated database: %v", err)
	}
	defer func() { _ = db.Close() }()
	var count int
	if err := db.QueryRow("SELECT count(*) FROM buckets").Scan(&count); err != nil {
		t.Fatalf("migrated schema is missing the buckets table: %v", err)
	}

	unknown := exec.Command(runtimeBinary, "frobnicate")
	unknown.Env = append(os.Environ(), "DFBG_DATABASE_URL="+adminURL)
	if output, err := unknown.CombinedOutput(); err == nil {
		t.Fatalf("unknown subcommand exited zero:\n%s", output)
	}
}
