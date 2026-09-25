package gpaorm

import (
	"context"
	"database/sql"
	"time"

	"github.com/lemmego/gpa"
	"github.com/lemmego/orm"
)

// ProviderName is how this provider identifies itself in the GPA registry.
// It must stay distinct from every other provider's name: the registry keys
// on this string, and GORM already occupies "GORM".
const ProviderName = "LemmegoORM"

// Version reported through ProviderInfo.
const Version = "0.1.0"

// Provider adapts an ORM connection to gpa.Provider and gpa.SQLProvider.
//
// It takes an already-open *orm.DB rather than dialling a database itself, so
// that gpaorm needs no SQL driver dependencies: the application chooses and
// imports its own driver, exactly as it does for the migration module.
type Provider struct {
	db     *orm.DB
	config gpa.Config
}

// New wraps an ORM connection.
func New(db *orm.DB) *Provider { return &Provider{db: db} }

// ORM returns the underlying ORM handle, for callers that want the richer API
// instead of the GPA contract.
func (p *Provider) ORM() *orm.DB { return p.db }

// Repository builds a typed repository. This is a generic method, legal in Go
// 1.27 on a concrete type; it is not part of any interface, which generic
// methods cannot satisfy.
func (p *Provider) Repository[T any]() gpa.MigratableRepository[T] {
	return NewRepository[T](p)
}

// GetRepository builds a repository from a provider.
func GetRepository[T any](provider *Provider) gpa.MigratableRepository[T] {
	return NewRepository[T](provider)
}

// GetRepositoryByName resolves a registered provider and builds a repository
// from it, mirroring the other GPA providers' entry point.
func GetRepositoryByName[T any](instanceName ...string) gpa.MigratableRepository[T] {
	return NewRepository[T](gpa.MustGet[*Provider](instanceName...))
}

// =====================================
// gpa.Provider
// =====================================

// Configure applies connection-pool settings. It cannot re-point an existing
// connection at a different database; construct a new provider for that.
func (p *Provider) Configure(config gpa.Config) error {
	p.config = config
	sqlDB := p.db.SQLDB()
	if sqlDB == nil {
		return gpa.NewError(gpa.ErrorTypeConnection, "gpaorm: provider has no connection")
	}
	if config.MaxOpenConns > 0 {
		sqlDB.SetMaxOpenConns(config.MaxOpenConns)
	}
	if config.MaxIdleConns > 0 {
		sqlDB.SetMaxIdleConns(config.MaxIdleConns)
	}
	if config.ConnMaxLifetime > 0 {
		sqlDB.SetConnMaxLifetime(config.ConnMaxLifetime)
	}
	if config.ConnMaxIdleTime > 0 {
		sqlDB.SetConnMaxIdleTime(config.ConnMaxIdleTime)
	}
	return nil
}

func (p *Provider) Health() error {
	sqlDB := p.db.SQLDB()
	if sqlDB == nil {
		return gpa.NewError(gpa.ErrorTypeConnection, "gpaorm: provider has no connection")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := sqlDB.PingContext(ctx); err != nil {
		return gpa.NewErrorWithCause(gpa.ErrorTypeConnection, "gpaorm: ping failed", err)
	}
	return nil
}

func (p *Provider) Close() error {
	if sqlDB := p.db.SQLDB(); sqlDB != nil {
		return sqlDB.Close()
	}
	return nil
}

// SupportedFeatures lists only what the adapter actually implements. Locking,
// subqueries and migration are absent on purpose: the corresponding calls
// return an unsupported error rather than quietly doing nothing.
func (p *Provider) SupportedFeatures() []gpa.Feature {
	return []gpa.Feature{
		gpa.FeatureTransactions,
		gpa.FeatureJoins,
		gpa.FeatureRawSQL,
		gpa.FeatureIndexes,
		gpa.FeatureAggregation,
	}
}

func (p *Provider) ProviderInfo() gpa.ProviderInfo {
	return gpa.ProviderInfo{
		Name:         ProviderName,
		Version:      Version,
		DatabaseType: gpa.DatabaseTypeSQL,
		Features:     p.SupportedFeatures(),
	}
}

// =====================================
// gpa.SQLProvider
// =====================================

// DB returns the underlying *sql.DB, as the SQLProvider contract specifies.
// Use ORM() for the typed handle.
func (p *Provider) DB() any { return p.db.SQLDB() }

// BeginTx starts a transaction and returns it as *orm.Tx.
func (p *Provider) BeginTx(ctx context.Context, opts *gpa.TxOptions) (any, error) {
	var sqlOpts *sql.TxOptions
	if opts != nil {
		sqlOpts = &sql.TxOptions{
			ReadOnly:  opts.ReadOnly,
			Isolation: isolationLevel(opts.IsolationLevel),
		}
	}
	tx, err := p.db.BeginTx(ctx, sqlOpts)
	if err != nil {
		return nil, translateError(err)
	}
	return tx, nil
}

// Migrate is not implemented: the ORM does not derive DDL from structs.
func (p *Provider) Migrate(models ...any) error {
	return unsupported("Migrate is not implemented; use github.com/lemmego/migration")
}

func (p *Provider) RawQuery(ctx context.Context, query string, args ...any) (any, error) {
	rows, err := p.db.SQLDB().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, translateError(p.db.Dialect().ClassifyError(err))
	}
	return rows, nil
}

func (p *Provider) RawExec(ctx context.Context, query string, args ...any) (gpa.Result, error) {
	result, err := p.db.Exec(ctx, query, args...)
	if err != nil {
		return nil, translateError(err)
	}
	return result, nil
}

func isolationLevel(level gpa.IsolationLevel) sql.IsolationLevel {
	switch level {
	case gpa.IsolationReadUncommitted:
		return sql.LevelReadUncommitted
	case gpa.IsolationReadCommitted:
		return sql.LevelReadCommitted
	case gpa.IsolationRepeatableRead:
		return sql.LevelRepeatableRead
	case gpa.IsolationSerializable:
		return sql.LevelSerializable
	}
	return sql.LevelDefault
}

// Compile-time proof that the adapter satisfies every contract it claims.
var (
	_ gpa.Provider                  = (*Provider)(nil)
	_ gpa.SQLProvider               = (*Provider)(nil)
	_ gpa.Repository[any]           = (*Repository[any])(nil)
	_ gpa.SQLRepository[any]        = (*Repository[any])(nil)
	_ gpa.MigratableRepository[any] = (*Repository[any])(nil)
	_ gpa.Transaction[any]          = (*Transaction[any])(nil)
)
