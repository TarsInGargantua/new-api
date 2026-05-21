package model

import (
	"net"
	"net/url"
	"os"
	"strings"

	drivermysql "github.com/go-sql-driver/mysql"
)

var databaseDSNEnvKeys = []string{
	"MYSQL_DSN",
	"MYSQL_URL",
	"MYSQL_URI",
	"DATABASE_URL",
}

var mysqlPartEnvKeys = []string{
	"MYSQL_HOST",
	"MYSQL_PORT",
	"MYSQL_USERNAME",
	"MYSQL_USER",
	"MYSQL_PASSWORD",
	"MYSQL_ROOT_PASSWORD",
	"MYSQL_DATABASE",
	"MYSQL_DB",
}

func resolveConfiguredDSN(envName string) string {
	dsn := strings.TrimSpace(os.Getenv(envName))
	if dsn == "" && envName == "SQL_DSN" {
		dsn = resolveDatabaseDSNFromEnv()
	}
	if strings.HasPrefix(strings.ToLower(dsn), "mysql://") {
		if converted, ok := mysqlURLToDriverDSN(dsn); ok {
			return converted
		}
	}
	return dsn
}

func resolveDatabaseDSNFromEnv() string {
	for _, key := range databaseDSNEnvKeys {
		value := strings.TrimSpace(os.Getenv(key))
		if value == "" {
			continue
		}
		if key == "DATABASE_URL" && !isSupportedDatabaseURL(value) {
			continue
		}
		if strings.HasPrefix(strings.ToLower(value), "mysql://") {
			if converted, ok := mysqlURLToDriverDSN(value); ok {
				return converted
			}
		}
		return value
	}

	host := firstNonEmptyEnv("MYSQL_HOST")
	if host == "" {
		return ""
	}
	user := firstNonEmptyEnv("MYSQL_USERNAME", "MYSQL_USER")
	database := firstNonEmptyEnv("MYSQL_DATABASE", "MYSQL_DB")
	if user == "" || database == "" {
		return ""
	}

	cfg := drivermysql.NewConfig()
	cfg.User = user
	cfg.Passwd = firstNonEmptyEnv("MYSQL_PASSWORD", "MYSQL_ROOT_PASSWORD")
	cfg.Net = "tcp"
	cfg.Addr = mysqlAddr(host, firstNonEmptyEnv("MYSQL_PORT"))
	cfg.DBName = database
	cfg.ParseTime = true
	cfg.Params = map[string]string{
		"charset": "utf8mb4",
		"loc":     "Local",
	}
	return cfg.FormatDSN()
}

func mysqlURLToDriverDSN(raw string) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil || !strings.EqualFold(u.Scheme, "mysql") {
		return "", false
	}

	cfg := drivermysql.NewConfig()
	cfg.User = u.User.Username()
	cfg.Passwd, _ = u.User.Password()
	cfg.Net = "tcp"
	cfg.Addr = mysqlAddr(u.Hostname(), u.Port())
	cfg.DBName = strings.TrimPrefix(u.Path, "/")
	cfg.ParseTime = true
	cfg.Params = map[string]string{
		"charset": "utf8mb4",
		"loc":     "Local",
	}
	for key, values := range u.Query() {
		if len(values) == 0 {
			continue
		}
		switch strings.ToLower(key) {
		case "parsetime":
			cfg.ParseTime = strings.EqualFold(values[len(values)-1], "true")
		default:
			cfg.Params[key] = values[len(values)-1]
		}
	}
	return cfg.FormatDSN(), true
}

func ensureMySQLDSNDefaults(dsn string) string {
	if dsn == "" {
		return dsn
	}
	lowerDSN := strings.ToLower(dsn)
	if !strings.Contains(lowerDSN, "parsetime=") {
		dsn = appendDSNParam(dsn, "parseTime", "true")
		lowerDSN = strings.ToLower(dsn)
	}
	if !strings.Contains(lowerDSN, "charset=") && !strings.Contains(lowerDSN, "collation=") {
		dsn = appendDSNParam(dsn, "charset", "utf8mb4")
		lowerDSN = strings.ToLower(dsn)
	}
	if !strings.Contains(lowerDSN, "loc=") {
		dsn = appendDSNParam(dsn, "loc", "Local")
	}
	return dsn
}

func appendDSNParam(dsn, key, value string) string {
	separator := "?"
	if strings.Contains(dsn, "?") {
		separator = "&"
	}
	return dsn + separator + key + "=" + url.QueryEscape(value)
}

func mysqlAddr(host, port string) string {
	host = strings.TrimSpace(host)
	port = strings.TrimSpace(port)
	if host == "" {
		return ""
	}
	if _, _, err := net.SplitHostPort(host); err == nil {
		return host
	}
	if port == "" {
		port = "3306"
	}
	return net.JoinHostPort(host, port)
}

func firstNonEmptyEnv(keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return ""
}

func isSupportedDatabaseURL(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	return strings.HasPrefix(value, "mysql://") ||
		strings.HasPrefix(value, "postgres://") ||
		strings.HasPrefix(value, "postgresql://")
}
