package store

import (
	"testing"
)

func TestOpen_Postgres_InvalidDriver(t *testing.T) {
	// OpenPostgres will fail because no postgres driver is
	// registered, but the registration itself should work.
	_, err := Open("postgres", "postgres://localhost/test?sslmode=disable")
	if err == nil {
		t.Fatal("expected error for unregistered postgres driver")
	}
	// The error should mention the driver, not "unknown driver".
	if got := err.Error(); got == "store: unknown driver \"postgres\"" {
		t.Fatalf("postgres should be registered, got: %s", got)
	}
}

func TestOpen_CaseInsensitive(t *testing.T) {
	// "SQLite" (capital S) should resolve to the sqlite driver.
	_, err := Open("SQLite", ":memory:")
	if err != nil {
		t.Fatalf("case-insensitive open: %v", err)
	}
}

func TestOpen_UnknownDriver(t *testing.T) {
	_, err := Open("nope", "")
	if err == nil {
		t.Fatal("expected error for unknown driver")
	}
}
