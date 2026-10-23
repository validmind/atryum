package store

import (
	"database/sql"
	"testing"
	"time"
)

// Unset pool fields take the defaults; set ones win, the option over config.
func TestConfigurePool(t *testing.T) {
	config := DBPool{MaxOpenConns: 40, ConnMaxLifetime: time.Minute}
	option := DBPool{MaxOpenConns: 8}
	if got, want := DefaultDBPool.Over(config.Over(option)), (DBPool{MaxOpenConns: 8, MaxIdleConns: 10, ConnMaxIdleTime: 5 * time.Minute, ConnMaxLifetime: time.Minute}); got != want {
		t.Fatalf("pool = %+v, want %+v", got, want)
	}

	db, err := sql.Open("pgx", "postgresql://unused@127.0.0.1:1/x") // never connects
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ConfigurePool(db, DBPool{})
	if got := db.Stats().MaxOpenConnections; got != DefaultDBPool.MaxOpenConns {
		t.Fatalf("MaxOpenConnections = %d, want %d", got, DefaultDBPool.MaxOpenConns)
	}
}
