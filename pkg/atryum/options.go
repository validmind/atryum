package atryum

import (
	"context"
	"database/sql"
	"net/http"

	"github.com/validmind/atryum/internal/invocation"
	"github.com/validmind/atryum/pkg/migrations"
)

// The types an embedding program's decider and event hook work with. They
// are aliases, so the invocation service uses them without conversion.
type (
	// Decider decides every tool call; see WithDecider.
	Decider = invocation.Decider
	// ToolCall is the call a Decider is asked about.
	ToolCall = invocation.ToolCall
	// Decision is a Decider's result.
	Decision = invocation.Decision
	// Verdict is allow, notify, deny or pending.
	Verdict = invocation.Verdict
	// EventHook receives every invocation change inside its transaction.
	EventHook = invocation.EventHook
	// Change is one invocation change: Atryum's event type, payload and the
	// invocation as written.
	Change = invocation.Change
	// Invocation is the invocation row as written.
	Invocation = invocation.Invocation
	// Approval is how an invocation was decided (Invocation.Approval).
	Approval = invocation.Approval
	// Status is an invocation status.
	Status = invocation.Status
)

// ResolvePending approves or denies a call held for a person. Pass the
// transaction the embedder already holds to make the decision, its Atryum
// event and the event hook commit with the embedder's own writes (the
// embedder then commits, which releases the held call on whichever replica
// holds it); pass nil to let Atryum commit on its own. A call that is no
// longer pending (already decided, abandoned by its caller, or finished)
// returns an error wrapping ErrNotPending.
type ResolvePending func(ctx context.Context, tx *sql.Tx, invocationID string, approve bool, actor, reason string) error

// ErrNotPending is wrapped by ResolvePending when the call is no longer pending.
var ErrNotPending = invocation.ErrNotPending

const (
	VerdictAllow   = invocation.VerdictAllow
	VerdictNotify  = invocation.VerdictNotify
	VerdictDeny    = invocation.VerdictDeny
	VerdictPending = invocation.VerdictPending
)

// options collects the extension points an embedding program can configure
// before handing control to Main.
type options struct {
	extraRoutes         []func(mux *http.ServeMux)
	extensionMigrations []extensionMigrations
	databaseHooks       []func(db *sql.DB, usePostgres bool)
	thirdPartyNotices   string
	decider             Decider
	eventHook           EventHook
	databaseURL         string
	requireAgentAuth    bool
	stuckReports        bool
	resolveHooks        []func(ResolvePending)
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

// WithDecider hands every tool call on every path (MCP proxy, direct
// invocation, harness hook, Managed Agents bridge) to d, which returns allow,
// notify, deny, or pending with a review ID. Atryum's own rules, plans and
// default policy are skipped, a decider error denies the call, and only the
// agent-facing routes are mounted: no operator API and no UI.
func WithDecider(d Decider) Option {
	return func(o *options) { o.decider = d }
}

// WithEventHook calls h inside the database transaction that changes an
// invocation, with that transaction, once per Atryum event written (and once
// as "invocation.created" for the insert). An error from h rolls the change
// back; on the paths that would run a tool, the call then fails without
// running it.
func WithEventHook(h EventHook) Option {
	return func(o *options) { o.eventHook = h }
}

// WithDatabaseURL sets the database URL, overriding server.database_url in
// the config file, so an embedding program keeps one database setting for the
// server and its own commands.
func WithDatabaseURL(url string) Option {
	return func(o *options) { o.databaseURL = url }
}

// WithRequireAgentAuth makes agent authentication mandatory: the server
// refuses to start unless an [[auth]] issuer is enabled, refuses to start
// with auth_debug.skip_verify or ATRYUM_AUTH_DEBUG_SKIP_VERIFY set, and agent
// routes reject a request that carries no verified agent identity. A
// caller-supplied agent_id is then never used for an HTTP caller.
func WithRequireAgentAuth() Option {
	return func(o *options) { o.requireAgentAuth = true }
}

// WithStuckReports makes the MCP proxy offer the atryum_report_stuck tool
// (arguments task and boundary, up to 1 KB each, and an optional detail up to
// 4 KB), which an agent calls to report that it cannot finish within its
// authority. It authorizes nothing: the report is recorded as an invocation of
// that tool in status "reported", the event hook sees only
// "invocation.stuck_reported" (no "invocation.created"), and the agent gets a
// short acknowledgement. The server in the MCP path must be a configured one.
func WithStuckReports() Option {
	return func(o *options) { o.stuckReports = true }
}

// WithResolvePending hands the embedding program a ResolvePending once the
// invocation service is built, before the server accepts requests.
func WithResolvePending(receive func(ResolvePending)) Option {
	return func(o *options) {
		if receive != nil {
			o.resolveHooks = append(o.resolveHooks, receive)
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
