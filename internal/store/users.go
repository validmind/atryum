package store

import (
	"context"
	"database/sql"
	"strings"
	"time"

	sq "github.com/Masterminds/squirrel"
	"github.com/google/uuid"
)

// User roles. Kept deliberately flat in the open core: admin sees and does
// everything, member is scoped to agents they belong to (see agent_members).
// Richer roles/groups live in embedding programs via the authz seam.
const (
	UserRoleAdmin  = "admin"
	UserRoleMember = "member"
)

// Where a user's role came from. The IdP admin claim is authoritative until
// an operator sets the role by hand; after that logins stop touching it, so
// a promotion made in the Users UI is not silently undone at next sign-in.
const (
	UserRoleSourceIdP    = "idp"
	UserRoleSourceManual = "manual"
)

// User is a principal seen at the operator API. Identity is (Issuer, Subject);
// Email and Name are display-only and refreshed on every login.
type User struct {
	ID          string
	Issuer      string
	Subject     string
	Email       string
	Name        string
	Role        string
	RoleSource  string
	CreatedAt   time.Time
	LastLoginAt *time.Time
	DisabledAt  *time.Time
}

// Disabled reports whether the user has been disabled by an operator.
func (u User) Disabled() bool { return u.DisabledAt != nil }

// UsersRepo provides just-in-time provisioning and lookups for the users table.
type UsersRepo struct {
	db      *sql.DB
	sb      sq.StatementBuilderType
	dialect Dialect
}

func NewUsersRepo(db *sql.DB) *UsersRepo {
	return NewUsersRepoWithDialect(db, DialectSQLite)
}

func NewUsersRepoWithDialect(db *sql.DB, dialect Dialect) *UsersRepo {
	return &UsersRepo{db: db, sb: statementBuilderForDialect(dialect), dialect: dialect}
}

var userColumns = []string{
	"id", "issuer", "subject", "email", "name", "role", "role_source", "created_at", "last_login_at", "disabled_at",
}

// UpsertLogin records a login for (issuer, subject): inserts the user on first
// sight, otherwise refreshes email, name, role and last_login_at. Role is
// refreshed from the IdP claim on every login while role_source is 'idp';
// once an operator has set it (SetRole) the login leaves it alone.
// disabled_at is never touched here (it is an operator action).
// Empty email/name never overwrite known values, so a token without those
// claims does not erase what an earlier login (or userinfo enrichment) found.
// Returns the resulting row.
func (r *UsersRepo) UpsertLogin(ctx context.Context, issuer, subject, email, name, role string) (User, error) {
	issuer = strings.TrimRight(strings.TrimSpace(issuer), "/")
	subject = strings.TrimSpace(subject)
	if role != UserRoleAdmin {
		role = UserRoleMember
	}
	now := time.Now().UTC()

	existing, err := r.GetByIssuerSubject(ctx, issuer, subject)
	switch {
	case err == nil:
		if strings.TrimSpace(email) == "" {
			email = existing.Email
		}
		if strings.TrimSpace(name) == "" {
			name = existing.Name
		}
		ub := r.sb.Update("users").
			Set("email", email).
			Set("name", name).
			Set("last_login_at", now).
			Where(sq.Eq{"id": existing.ID})
		if existing.RoleSource != UserRoleSourceManual {
			ub = ub.Set("role", role)
		}
		update, args, err := ub.ToSql()
		if err != nil {
			return User{}, err
		}
		if _, err := r.db.ExecContext(ctx, update, args...); err != nil {
			return User{}, err
		}
		return r.Get(ctx, existing.ID)
	case err == sql.ErrNoRows:
		id := uuid.NewString()
		insert, args, err := r.sb.Insert("users").
			Columns(userColumns...).
			Values(id, issuer, subject, email, name, role, UserRoleSourceIdP, now, now, nil).
			ToSql()
		if err != nil {
			return User{}, err
		}
		if _, err := r.db.ExecContext(ctx, insert, args...); err != nil {
			return User{}, err
		}
		return r.Get(ctx, id)
	default:
		return User{}, err
	}
}

func (r *UsersRepo) Get(ctx context.Context, id string) (User, error) {
	query, args, err := r.sb.Select(userColumns...).From("users").Where(sq.Eq{"id": id}).ToSql()
	if err != nil {
		return User{}, err
	}
	return scanUser(r.db.QueryRowContext(ctx, query, args...))
}

func (r *UsersRepo) GetByIssuerSubject(ctx context.Context, issuer, subject string) (User, error) {
	issuer = strings.TrimRight(strings.TrimSpace(issuer), "/")
	query, args, err := r.sb.Select(userColumns...).From("users").
		Where(sq.Eq{"issuer": issuer, "subject": strings.TrimSpace(subject)}).
		ToSql()
	if err != nil {
		return User{}, err
	}
	return scanUser(r.db.QueryRowContext(ctx, query, args...))
}

func (r *UsersRepo) List(ctx context.Context) ([]User, error) {
	query, args, err := r.sb.Select(userColumns...).From("users").
		OrderBy("created_at ASC", "id ASC").ToSql()
	if err != nil {
		return nil, err
	}
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// SetDisabled disables (or re-enables) a user. Disabling does not delete
// anything: keys the user created stop validating because the runtime check
// joins on users.disabled_at, and callers are expected to also revoke them
// explicitly for the audit trail (see APIKeysRepo.RevokeByCreator).
func (r *UsersRepo) SetDisabled(ctx context.Context, id string, disabled bool) error {
	var value any
	if disabled {
		value = time.Now().UTC()
	}
	query, args, err := r.sb.Update("users").Set("disabled_at", value).Where(sq.Eq{"id": id}).ToSql()
	if err != nil {
		return err
	}
	res, err := r.db.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// SetRole sets the role by operator action and marks it manual, so later
// logins do not reset it from the IdP claim (see UpsertLogin).
func (r *UsersRepo) SetRole(ctx context.Context, id, role string) error {
	if role != UserRoleAdmin {
		role = UserRoleMember
	}
	query, args, err := r.sb.Update("users").
		Set("role", role).
		Set("role_source", UserRoleSourceManual).
		Where(sq.Eq{"id": id}).ToSql()
	if err != nil {
		return err
	}
	res, err := r.db.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func scanUser(row interface{ Scan(dest ...any) error }) (User, error) {
	var u User
	var lastLogin, disabled sql.NullTime
	if err := row.Scan(&u.ID, &u.Issuer, &u.Subject, &u.Email, &u.Name, &u.Role, &u.RoleSource, &u.CreatedAt, &lastLogin, &disabled); err != nil {
		return User{}, err
	}
	if lastLogin.Valid {
		t := lastLogin.Time
		u.LastLoginAt = &t
	}
	if disabled.Valid {
		t := disabled.Time
		u.DisabledAt = &t
	}
	return u, nil
}
