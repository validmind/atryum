package store

import (
	"database/sql"
	"fmt"
	"net/url"
	"strings"
	"time"

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

// DBPool sizes a PostgreSQL connection pool. A zero field means "unset".
type DBPool struct {
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxIdleTime time.Duration
	ConnMaxLifetime time.Duration
}

// DefaultDBPool keeps one process well inside a stock Postgres
// max_connections (100) with a few replicas, and recycles idle and old
// connections so failovers and pooler restarts are picked up. The decision
// listener holds one more connection outside the pool.
var DefaultDBPool = DBPool{MaxOpenConns: 20, MaxIdleConns: 10, ConnMaxIdleTime: 5 * time.Minute, ConnMaxLifetime: 30 * time.Minute}

// Over returns p with every set field of over replacing p's.
func (p DBPool) Over(over DBPool) DBPool {
	if over.MaxOpenConns != 0 {
		p.MaxOpenConns = over.MaxOpenConns
	}
	if over.MaxIdleConns != 0 {
		p.MaxIdleConns = over.MaxIdleConns
	}
	if over.ConnMaxIdleTime != 0 {
		p.ConnMaxIdleTime = over.ConnMaxIdleTime
	}
	if over.ConnMaxLifetime != 0 {
		p.ConnMaxLifetime = over.ConnMaxLifetime
	}
	return p
}

// ConfigurePool applies DefaultDBPool overlaid with p to db.
func ConfigurePool(db *sql.DB, p DBPool) {
	p = DefaultDBPool.Over(p)
	db.SetMaxOpenConns(p.MaxOpenConns)
	db.SetMaxIdleConns(p.MaxIdleConns)
	db.SetConnMaxIdleTime(p.ConnMaxIdleTime)
	db.SetConnMaxLifetime(p.ConnMaxLifetime)
}
