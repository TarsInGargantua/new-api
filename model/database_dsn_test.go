package model

import (
	"strings"
	"testing"
)

func clearDatabaseEnv(t *testing.T) {
	t.Helper()
	keys := append([]string{}, databaseDSNEnvKeys...)
	keys = append(keys, mysqlPartEnvKeys...)
	keys = append(keys, "SQL_DSN", "LOG_SQL_DSN")
	for _, key := range keys {
		t.Setenv(key, "")
	}
}

func TestResolveConfiguredDSNUsesExplicitSQLDSN(t *testing.T) {
	clearDatabaseEnv(t)
	t.Setenv("SQL_DSN", "root:pass@tcp(mysql:3306)/newapi")
	t.Setenv("MYSQL_HOST", "mysql.zeabur.internal")
	t.Setenv("MYSQL_USERNAME", "zeabur")
	t.Setenv("MYSQL_DATABASE", "ignored")

	got := resolveConfiguredDSN("SQL_DSN")
	if got != "root:pass@tcp(mysql:3306)/newapi" {
		t.Fatalf("expected explicit SQL_DSN, got %q", got)
	}
}

func TestResolveConfiguredDSNConvertsMySQLURL(t *testing.T) {
	clearDatabaseEnv(t)
	t.Setenv("SQL_DSN", "mysql://user:pass@mysql.example.com:3307/newapi?charset=utf8mb4")

	got := resolveConfiguredDSN("SQL_DSN")
	if !strings.HasPrefix(got, "user:pass@tcp(mysql.example.com:3307)/newapi?") {
		t.Fatalf("unexpected converted dsn prefix: %q", got)
	}
	for _, want := range []string{"charset=utf8mb4", "parseTime=true", "loc=Local"} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected %q in dsn %q", want, got)
		}
	}
}

func TestResolveConfiguredDSNBuildsFromMySQLEnv(t *testing.T) {
	clearDatabaseEnv(t)
	t.Setenv("MYSQL_HOST", "mysql.zeabur.internal")
	t.Setenv("MYSQL_PORT", "3306")
	t.Setenv("MYSQL_USERNAME", "zeabur")
	t.Setenv("MYSQL_PASSWORD", "secret")
	t.Setenv("MYSQL_DATABASE", "newapi")

	got := resolveConfiguredDSN("SQL_DSN")
	if !strings.HasPrefix(got, "zeabur:secret@tcp(mysql.zeabur.internal:3306)/newapi?") {
		t.Fatalf("unexpected dsn prefix: %q", got)
	}
	for _, want := range []string{"charset=utf8mb4", "parseTime=true", "loc=Local"} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected %q in dsn %q", want, got)
		}
	}
}

func TestResolveConfiguredDSNUsesMySQLConnectionString(t *testing.T) {
	clearDatabaseEnv(t)
	t.Setenv("MYSQL_CONNECTION_STRING", "mysql://user:pass@mysql.zeabur.internal:3306/newapi")

	got := resolveConfiguredDSN("SQL_DSN")
	if !strings.HasPrefix(got, "user:pass@tcp(mysql.zeabur.internal:3306)/newapi?") {
		t.Fatalf("unexpected converted dsn prefix: %q", got)
	}
	for _, want := range []string{"charset=utf8mb4", "parseTime=true", "loc=Local"} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected %q in dsn %q", want, got)
		}
	}
}

func TestResolveConfiguredDSNIgnoresUnsupportedDatabaseURL(t *testing.T) {
	clearDatabaseEnv(t)
	t.Setenv("DATABASE_URL", "redis://redis:6379")

	got := resolveConfiguredDSN("SQL_DSN")
	if got != "" {
		t.Fatalf("expected unsupported DATABASE_URL to be ignored, got %q", got)
	}
}
