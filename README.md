# gpaorm

The GPA provider backed by the [Lemmego ORM](../orm).

```text
gpaorm -> orm + gpa
```

The ORM never imports GPA, so every translation between the two lives here.

## Usage

`gpaorm` deliberately takes an already-open ORM connection rather than dialling
a database itself, so it needs no SQL driver dependencies — the application
chooses and imports its own driver, exactly as it does for the migration
module.

```go
sqlDB, _ := sql.Open("pgx", dsn)
provider := gpaorm.New(orm.Open(sqlDB, orm.Postgres()))

gpa.RegisterDefault(provider)                  // or gpa.Register("primary", provider)

users := gpaorm.GetRepository[User](provider)  // gpa.MigratableRepository[User]
users = gpaorm.GetRepositoryByName[User]()     // resolved from the registry
users = provider.Repository[User]()            // generic method, Go 1.27
```

`provider.ORM()` returns the underlying `*orm.DB` when you want the richer API;
`provider.DB()` returns the `*sql.DB`, as the `gpa.SQLProvider` contract
specifies.

The provider registers under the name `LemmegoORM`. The GPA registry keys on
that string, and `GORM` is already taken.

## What it implements

`Repository[T]` satisfies `gpa.Repository[T]`, `gpa.SQLRepository[T]` and
`gpa.MigratableRepository[T]`; `Transaction[T]` satisfies `gpa.Transaction[T]`;
`Provider` satisfies `gpa.Provider` and `gpa.SQLProvider`. All six are asserted
at compile time in `provider.go`.

Query options translate as follows:

| GPA | ORM |
|---|---|
| `Where`, `WhereIn`, `WhereLike`, `WhereNull` | predicates |
| `And`, `Or`, `Not` composites | `orm.And` / `orm.Or` / `orm.Not` |
| `OrderBy`, `Limit`, `Offset`, `Distinct` | the same |
| `Fields` | `Select` |
| `Join`, `LeftJoin` | `JoinTable` / `LeftJoinTable` |
| `GroupBy`, `Having` | the same |
| `Preload` | `With` — one query per relation level |

Field names may be given either as struct field names or as column names; both
resolve.

## What it refuses

Anything the adapter cannot express returns `gpa.ErrorTypeUnsupported` rather
than being silently dropped:

- **Row locking** (`Lock`). A lock that is quietly ignored leaves the caller
  believing they hold one.
- **Subqueries** (`SubQueries`, `SubQueryCondition`).
- **`CreateTable`, `MigrateTable`, `GetTableInfo`, `GetMigrationStatus`,
  `Migrate`.** The ORM does not generate DDL from struct definitions; schema is
  owned by `github.com/lemmego/migration`, the framework's single source of
  truth for it. `DropTable`, `CreateIndex` and `DropIndex` are implemented,
  since they need no schema inference.

Composite `AND`/`OR` conditions **are** translated. GPA's GORM provider drops
them in its `default:` branch, which silently widens a query instead of
narrowing it.

## Transactions

GPA models a transaction as a per-entity-type repository (`Transaction[T]`
embeds `Repository[T]`), while the ORM's transaction is deliberately
entity-agnostic so one unit of work can span several models. The bridge is that
many `Transaction[T]` values can share one `*orm.Tx`:

```go
err := users.Transaction(ctx, func(tx gpa.Transaction[User]) error {
    user := &User{Email: "owner@example.com"}
    if err := tx.Create(ctx, user); err != nil {
        return err
    }
    posts := gpaorm.TxFor[Post](tx.(*gpaorm.Transaction[User]))
    return posts.Create(ctx, &Post{UserID: user.ID})
})
```

`Commit` is a no-op: the transaction commits when the callback returns nil.
`Rollback` marks the transaction for rollback and is **not** reported as an
error, since it is what the caller asked for. Savepoints are issued as SQL, so
savepoint names are validated as plain identifiers — they cannot be bound as
parameters.

## Errors

| ORM | GPA |
|---|---|
| `ErrNotFound` | `ErrorTypeNotFound` |
| `*ConstraintError` (unique) | `ErrorTypeDuplicate` |
| `*ConstraintError` (other) | `ErrorTypeConstraint` |
| `ErrUnsupported` | `ErrorTypeUnsupported` |
| `ErrUnsafeMutation` | `ErrorTypeInvalidArgument` |
| `ErrClosed` | `ErrorTypeTransaction` |
| anything else | `ErrorTypeDatabase` |

The original error is always kept as the cause, so `errors.As` still reaches
`*orm.ConstraintError` and the driver error beneath it.

## Hooks

The ORM's `BeforeCreate`/`AfterCreate`/`BeforeUpdate`/`AfterUpdate`/
`BeforeDelete`/`AfterDelete` interfaces are signature-identical to GPA's, so an
entity implementing one implements the other and the ORM runs them itself. The
adapter adds only what GPA declares and the ORM does not: `Validate` before
create and update, and `AfterFind` on results.

Note one behavioural difference from `gpagorm`: an after-hook error is
**returned** here, whereas `gpagorm` logs and swallows it.

## Verification

```bash
cd gpaorm
go build ./... && go vet ./... && go test ./... && go test -race ./...
```
