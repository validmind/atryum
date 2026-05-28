package store

import (
	"database/sql"
	"fmt"
	"net/url"
	"strings"

	sq "github.com/Masterminds/squirrel"
)

type Dialect string

const (
	DialectSQLite   Dialect = "sqlite"
	DialectPostgres Dialect = "postgres"
)

type DBTarget struct {
	DriverName string
	DSN        string
	Dialect    Dialect
}

func ResolveDBTarget(databaseURL, databasePath string) (DBTarget, error) {
	if databasePath == "" {
		databasePath = "./atryum.db"
	}
	if strings.TrimSpace(databaseURL) == "" {
		return DBTarget{DriverName: "sqlite", DSN: databasePath, Dialect: DialectSQLite}, nil
	}

	u, err := url.Parse(databaseURL)
	if err != nil {
		return DBTarget{}, fmt.Errorf("parse database_url: %w", err)
	}

	scheme := strings.ToLower(u.Scheme)
	switch scheme {
	case "postgres", "postgresql":
		return DBTarget{DriverName: "pgx", DSN: databaseURL, Dialect: DialectPostgres}, nil
	case "sqlite":
		dsn := sqliteDSNFromURL(u)
		if dsn == "" {
			dsn = databasePath
		}
		return DBTarget{DriverName: "sqlite", DSN: dsn, Dialect: DialectSQLite}, nil
	case "file", "":
		return DBTarget{DriverName: "sqlite", DSN: databaseURL, Dialect: DialectSQLite}, nil
	default:
		return DBTarget{}, fmt.Errorf("unsupported database_url scheme %q", u.Scheme)
	}
}

func OpenDatabase(databaseURL, databasePath string) (*sql.DB, Dialect, error) {
	target, err := ResolveDBTarget(databaseURL, databasePath)
	if err != nil {
		return nil, "", err
	}
	db, err := sql.Open(target.DriverName, target.DSN)
	if err != nil {
		return nil, "", err
	}
	// SQLite does not support concurrent writers. Limiting to a single
	// connection serialises all reads and writes through the same handle so
	// that writes are immediately visible to subsequent reads.
	if target.Dialect == DialectSQLite {
		db.SetMaxOpenConns(1)
	}
	return db, target.Dialect, nil
}

func statementBuilderForDialect(d Dialect) sq.StatementBuilderType {
	if d == DialectPostgres {
		return sq.StatementBuilder.PlaceholderFormat(sq.Dollar)
	}
	return sq.StatementBuilder.PlaceholderFormat(sq.Question)
}

func sqliteDSNFromURL(u *url.URL) string {
	if u.Opaque != "" {
		return u.Opaque
	}
	if u.Host != "" && u.Path != "" {
		return u.Host + u.Path
	}
	if u.Host != "" {
		return u.Host
	}
	return u.Path
}
