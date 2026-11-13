package store

import (
	"context"
	"database/sql"
	"encoding/json"

	sq "github.com/Masterminds/squirrel"
)

// RedactAssertedIdentity removes what identifies one application end user
// from the invocations an application (agent by) made for them, found by
// their key (invocation.EndUserKey): the asserted id, display name, email,
// roles, end-user tenant and the key itself go, and the "asserted" block
// leaves those invocations' event payloads. What the call was (tool,
// arguments, decision) stays. It runs in one transaction, within the tenant
// ctx names, and returns how many invocations it changed.
func RedactAssertedIdentity(ctx context.Context, db *sql.DB, dialect Dialect, by, userKey string) (int, error) {
	if by == "" || userKey == "" {
		return 0, nil
	}
	sb := statementBuilderForDialect(dialect)
	n := 0
	err := InTx(ctx, db, func(ctx context.Context, tx *sql.Tx) error {
		query, args, err := sb.Select("invocation_id").From("invocations").
			Where(sq.Eq{"asserted_by": by, "asserted_user_key": userKey}).ToSql()
		if err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, query, args...)
		if err != nil {
			return err
		}
		var ids []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if len(ids) == 0 {
			return nil
		}
		query, args, err = sb.Update("invocations").
			Set("asserted_user_id", nil).Set("asserted_user_display", nil).Set("asserted_user_email", nil).
			Set("asserted_roles", nil).Set("asserted_eu_tenant", nil).Set("asserted_user_key", nil).
			Where(sq.Eq{"invocation_id": ids}).ToSql()
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, query, args...); err != nil {
			return err
		}
		if err := stripAssertedFromEvents(ctx, tx, sb, ids); err != nil {
			return err
		}
		n = len(ids)
		return nil
	})
	return n, err
}

// stripAssertedFromEvents rewrites the payload of every event of ids that
// carries an "asserted" block, without it.
func stripAssertedFromEvents(ctx context.Context, tx *sql.Tx, sb sq.StatementBuilderType, ids []string) error {
	query, args, err := sb.Select("id", "payload_json").From("invocation_events").
		Where(sq.Eq{"invocation_id": ids}).Where(sq.Like{"payload_json": `%"asserted"%`}).ToSql()
	if err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	type rewrite struct {
		id      any
		payload string
	}
	var out []rewrite
	for rows.Next() {
		var id any
		var payload string
		if err := rows.Scan(&id, &payload); err != nil {
			rows.Close()
			return err
		}
		var m map[string]json.RawMessage
		if json.Unmarshal([]byte(payload), &m) != nil {
			continue // not an object: nothing of ours in it
		}
		if _, ok := m["asserted"]; !ok {
			continue
		}
		delete(m, "asserted")
		b, err := json.Marshal(m)
		if err != nil {
			rows.Close()
			return err
		}
		out = append(out, rewrite{id, string(b)})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, r := range out {
		query, args, err := sb.Update("invocation_events").Set("payload_json", r.payload).Where(sq.Eq{"id": r.id}).ToSql()
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, query, args...); err != nil {
			return err
		}
	}
	return nil
}
