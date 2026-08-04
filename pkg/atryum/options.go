package atryum

import (
	"database/sql"
	"net/http"

	"github.com/validmind/atryum/pkg/migrations"
)

// options collects the extension points an embedding program can configure
// before handing control to Main.
type options struct {
	extraRoutes         []func(mux *http.ServeMux)
	extensionMigrations []extensionMigrations
	databaseHooks       []func(db *sql.DB, usePostgres bool)
	thirdPartyNotices   string
}

type extensionMigrations struct {
	namespace   string
	definitions []migrations.Definition
}

// Option configures the atryum CLI when embedded in another program.
type Option func(*options)

// WithRoutes registers additional HTTP routes on the server mux. The callback
// runs after the built-in routes are registered and outside every auth
// middleware chain (like /healthz): each extra route is responsible for its
// own authentication. Patterns must not collide with built-in routes.
func WithRoutes(register func(mux *http.ServeMux)) Option {
	return func(o *options) {
		if register != nil {
			o.extraRoutes = append(o.extraRoutes, register)
		}
	}
}

// WithMigrations registers schema migrations owned by the embedding program.
// They run at server startup after all built-in atryum migrations, in the
// order the options were passed, and are tracked per (namespace, version) in
// the extension_schema_migrations table — a sequence independent of atryum's
// own, so each extension numbers its migrations from 1 and new upstream
// migrations can never collide with them. The namespace identifies the
// extension (e.g. "authority") and must stay stable across releases.
func WithMigrations(namespace string, definitions []migrations.Definition) Option {
	return func(o *options) {
		o.extensionMigrations = append(o.extensionMigrations, extensionMigrations{
			namespace:   namespace,
			definitions: definitions,
		})
	}
}

// WithDatabase hands the embedding program the opened database once all
// migrations (built-in and extension) have been applied, before the server
// starts accepting requests. usePostgres distinguishes the SQL dialect, with
// false meaning SQLite (the same convention as migrations.Step).
func WithDatabase(hook func(db *sql.DB, usePostgres bool)) Option {
	return func(o *options) {
		if hook != nil {
			o.databaseHooks = append(o.databaseHooks, hook)
		}
	}
}

// WithThirdPartyNotices sets the text printed by the `licenses` command.
// The stock atryum binary passes its embedded notices bundle; embedding
// programs should pass a bundle covering their full dependency tree.
func WithThirdPartyNotices(text string) Option {
	return func(o *options) {
		if text != "" {
			o.thirdPartyNotices = text
		}
	}
}

func buildOptions(opts []Option) options {
	o := options{
		thirdPartyNotices: "Third-party notices were not embedded in this build.\n",
	}
	for _, opt := range opts {
		opt(&o)
	}
	return o
}
