package store

import (
	"net/url"
	"strings"
	"testing"
)

func TestWithStatementTimeout_Appended(t *testing.T) {
	dsn := "postgres://u:p@host:5432/db?sslmode=disable"
	got := withStatementTimeout(dsn)
	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	opts := u.Query().Get("options")
	if !strings.Contains(opts, "statement_timeout=30000") {
		t.Fatalf("statement_timeout missing: %s", got)
	}
	if strings.ContainsAny(got, " \t") {
		t.Fatalf("options not URL-encoded: %s", got)
	}
}

func TestWithStatementTimeout_ExistingOptions(t *testing.T) {
	dsn := "postgres://u:p@host:5432/db?sslmode=disable&options=-c%20application_name%3Dllmrx"
	got := withStatementTimeout(dsn)
	if !strings.Contains(got, "statement_timeout") {
		t.Fatalf("statement_timeout missing: %s", got)
	}
	if !strings.Contains(got, "application_name") {
		t.Fatalf("existing options lost: %s", got)
	}
}

func TestWithStatementTimeout_AlreadySet(t *testing.T) {
	dsn := "postgres://u:p@host:5432/db?options=-c%20statement_timeout%3D5000"
	got := withStatementTimeout(dsn)
	u, _ := url.Parse(got)
	if !strings.Contains(u.Query().Get("options"), "statement_timeout=5000") {
		t.Fatalf("existing timeout lost: %s", got)
	}
	if strings.Count(got, "statement_timeout") != 1 {
		t.Fatalf("duplicate statement_timeout: %s", got)
	}
}

func TestWithStatementTimeout_InvalidDSN(t *testing.T) {
	if got := withStatementTimeout("::not-a-dsn::"); got != "::not-a-dsn::" {
		t.Fatalf("invalid dsn must pass through unchanged: %s", got)
	}
}
