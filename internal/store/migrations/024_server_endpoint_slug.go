package migrations

import (
	"database/sql"
	"fmt"
	"sort"

	"atryum/internal/mcp"
)

func migration024() Definition {
	return Definition{
		Version: 24,
		Name:    "024_server_endpoint_slug",
		Steps: []Step{
			// AddColumnIfMissing (not a bare ADD COLUMN): this migration has
			// already been renumbered once by a rebase (main inserted it
			// ahead of this branch's session migrations), so a database
			// stamped under the old version number can hit this ALTER a
			// second time. See AddColumnIfMissing's doc comment for why
			// idempotency is now a project-wide requirement for these steps.
			AddColumnIfMissing("mcp_servers", "endpoint_slug",
				"TEXT NOT NULL DEFAULT ''",
				"TEXT NOT NULL DEFAULT ''",
			),
			Custom("backfill server endpoint_slug", backfillServerEndpointSlugs),
			Raw("create unique server endpoint_slug index", `
				CREATE UNIQUE INDEX IF NOT EXISTS idx_mcp_servers_endpoint_slug ON mcp_servers(endpoint_slug)
			`),
		},
	}
}

func backfillServerEndpointSlugs(tx *sql.Tx, usePostgres bool) error {
	rows, err := tx.Query(`SELECT name, endpoint_slug FROM mcp_servers`)
	if err != nil {
		return err
	}
	type serverEndpointSlugRow struct {
		name string
	}
	var rowsToBackfill []serverEndpointSlugRow
	used := map[string]bool{}
	for rows.Next() {
		var name, endpointSlug string
		if err := rows.Scan(&name, &endpointSlug); err != nil {
			rows.Close()
			return err
		}
		if endpointSlug != "" {
			used[endpointSlug] = true
			continue
		}
		rowsToBackfill = append(rowsToBackfill, serverEndpointSlugRow{name: name})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}

	sort.Slice(rowsToBackfill, func(i, j int) bool {
		return rowsToBackfill[i].name < rowsToBackfill[j].name
	})

	updateSQL := `UPDATE mcp_servers SET endpoint_slug = ? WHERE name = ?`
	if usePostgres {
		updateSQL = `UPDATE mcp_servers SET endpoint_slug = $1 WHERE name = $2`
	}
	for _, row := range rowsToBackfill {
		slug := uniqueEndpointSlug(mcp.EndpointSlug(row.name), used)
		if _, err := tx.Exec(updateSQL, slug, row.name); err != nil {
			return err
		}
	}
	return nil
}

func uniqueEndpointSlug(base string, used map[string]bool) string {
	if base == "" {
		base = "server"
	}
	if !used[base] {
		used[base] = true
		return base
	}
	for i := 2; ; i++ {
		candidate := fmt.Sprintf("%s-%d", base, i)
		if !used[candidate] {
			used[candidate] = true
			return candidate
		}
	}
}
