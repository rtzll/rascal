package testdb

import (
	"database/sql"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"
)

var gooseDialectOnce sync.Once

func OpenMigratedSQLite(t *testing.T) *sql.DB {
	t.Helper()

	gooseDialectOnce.Do(func() {
		if err := goose.SetDialect("sqlite3"); err != nil {
			t.Fatalf("configure goose sqlite dialect: %v", err)
		}
	})

	path := filepath.Join(t.TempDir(), "state.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open sqlite db: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close db: %v", err)
		}
	})

	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	if _, err := db.Exec("PRAGMA journal_mode=WAL;"); err != nil {
		t.Fatalf("enable sqlite WAL mode: %v", err)
	}
	if _, err := db.Exec("PRAGMA busy_timeout=5000;"); err != nil {
		t.Fatalf("set sqlite busy_timeout: %v", err)
	}
	if _, err := db.Exec("PRAGMA foreign_keys=ON;"); err != nil {
		t.Fatalf("enable sqlite foreign_keys: %v", err)
	}

	if err := goose.Up(db, migrationsDir(t)); err != nil {
		t.Fatalf("run migrations: %v", err)
	}

	return db
}

func migrationsDir(t *testing.T) string {
	t.Helper()

	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve testdb source path")
	}
	return filepath.Join(filepath.Dir(filename), "..", "migrations")
}
