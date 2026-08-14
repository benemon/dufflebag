//go:build integration

package main

import (
	"context"
	"database/sql"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"

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

// A single-role deployment is hands-off: the serving process owns the schema
// and applies its own migrations at boot, so first boot from a blank database
// needs no migrate step. The owning role holds neither superuser nor
// BYPASSRLS, so FORCE ROW LEVEL SECURITY still binds it and the startup guard
// is unaffected — the subcommand remains the hardened variant, not a
// prerequisite.
func TestServerBootPreparesAFreshDatabaseSingleRole(t *testing.T) {
	ctx := context.Background()
	container, err := tcpostgres.Run(
		ctx,
		"postgres:17-alpine",
		tcpostgres.WithDatabase("postgres"),
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

	admin, err := sql.Open("pgx", adminURL)
	if err != nil {
		t.Fatalf("open admin connection: %v", err)
	}
	defer func() { _ = admin.Close() }()
	for _, statement := range []string{
		"CREATE ROLE handsoff LOGIN PASSWORD 'handsoff' NOSUPERUSER NOBYPASSRLS",
		"CREATE DATABASE handsoff OWNER handsoff",
	} {
		if _, err := admin.Exec(statement); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
	owner, err := url.Parse(adminURL)
	if err != nil {
		t.Fatalf("parse connection string: %v", err)
	}
	owner.User = url.UserPassword("handsoff", "handsoff")
	owner.Path = "/handsoff"

	address := reserveAddress(t)
	command := runtimeCommand(owner.String(), address, map[string]string{
		"DFBG_SHUTDOWN_GRACE_PERIOD": "1s",
	})
	output, err := os.CreateTemp(t.TempDir(), "dufflebag-*.log")
	if err != nil {
		t.Fatal(err)
	}
	command.Stdout = output
	command.Stderr = output
	if err := command.Start(); err != nil {
		t.Fatalf("start real dufflebag binary: %v", err)
	}
	t.Cleanup(func() {
		_ = command.Process.Kill()
		_ = command.Wait()
		_ = output.Close()
	})

	logs := func() string {
		_ = output.Sync()
		content, _ := os.ReadFile(output.Name())
		return string(content)
	}
	waitFor(t, 15*time.Second, func() bool {
		response, err := http.Get("http://" + address + "/sys/health")
		if err != nil {
			return false
		}
		_ = response.Body.Close()
		return true
	}, "boot from a blank single-role database never served; "+logs())

	ownerDB, err := sql.Open("pgx", owner.String())
	if err != nil {
		t.Fatalf("open single-role database: %v", err)
	}
	defer func() { _ = ownerDB.Close() }()
	var count int
	if err := ownerDB.QueryRow("SELECT count(*) FROM buckets").Scan(&count); err != nil {
		t.Fatalf("boot did not create the schema: %v\n%s", err, logs())
	}

	if err := command.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("stop real dufflebag binary: %v", err)
	}
	if err := command.Wait(); err != nil {
		t.Fatalf("real dufflebag binary shutdown: %v\n%s", err, logs())
	}
}
