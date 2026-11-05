package store

import (
	"testing"

	"github.com/jackc/pgx/v5"
)

// Embedders reach the pgx connection under database/sql's Raw, for LISTEN.
func TestTenantConnExposesPgxConn(t *testing.T) {
	var c any = &tenantConn{}
	if _, ok := c.(interface{ Conn() *pgx.Conn }); !ok {
		t.Fatal("tenantConn hides Conn() *pgx.Conn")
	}
}
