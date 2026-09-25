package gpaorm

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	"github.com/lemmego/gpa"
	"github.com/lemmego/orm"
)

// Repository implements the GPA repository contracts over the Lemmego ORM.
//
// It is a concrete generic type rather than a generic method on some shared
// interface: Go 1.27 generic methods cannot satisfy an interface, so this is
// the only shape that can implement gpa.Repository[T].
type Repository[T any] struct {
	provider *Provider
	db       *orm.DB
	tx       *orm.Tx
}

// NewRepository builds a repository bound to a provider's connection.
func NewRepository[T any](provider *Provider) *Repository[T] {
	return &Repository[T]{provider: provider, db: provider.db}
}

// query starts an ORM query on whichever handle is active, so every method
// below works identically inside and outside a transaction.
func (r *Repository[T]) query() *orm.Query[T] {
	if r.tx != nil {
		return r.tx.Model[T]()
	}
	return r.db.Model[T]()
}

func (r *Repository[T]) raw(query string, args ...any) *orm.RawQuery[T] {
	if r.tx != nil {
		return r.tx.Raw[T](query, args...)
	}
	return r.db.Raw[T](query, args...)
}

func (r *Repository[T]) exec(ctx context.Context, query string, args ...any) (gpa.Result, error) {
	if r.tx != nil {
		result, err := r.tx.Exec(ctx, query, args...)
		return result, translateError(err)
	}
	result, err := r.db.Exec(ctx, query, args...)
	return result, translateError(err)
}

func (r *Repository[T]) info() (*orm.ModelInfo, error) {
	return orm.ModelInfoFor[T]()
}

// =====================================
// Basic CRUD
// =====================================

// Create inserts an entity. The ORM runs the Before/AfterCreate hooks itself,
// since its hook interfaces are signature-identical to GPA's; only Validate
// is GPA-specific and therefore run here.
func (r *Repository[T]) Create(ctx context.Context, entity *T) error {
	if err := runValidate(ctx, entity); err != nil {
		return err
	}
	_, err := r.query().Create(ctx, entity)
	return translateError(err)
}

func (r *Repository[T]) CreateBatch(ctx context.Context, entities []*T) error {
	for _, entity := range entities {
		if err := runValidate(ctx, entity); err != nil {
			return err
		}
	}
	_, err := r.query().CreateBatch(ctx, entities)
	return translateError(err)
}

func (r *Repository[T]) FindByID(ctx context.Context, id any) (*T, error) {
	entity, err := r.query().Find(ctx, id)
	if err != nil {
		return nil, translateError(err)
	}
	if err := runAfterFind(ctx, entity); err != nil {
		return nil, err
	}
	return entity, nil
}

func (r *Repository[T]) FindAll(ctx context.Context, opts ...gpa.QueryOption) ([]*T, error) {
	return r.Query(ctx, opts...)
}

func (r *Repository[T]) Update(ctx context.Context, entity *T) error {
	if err := runValidate(ctx, entity); err != nil {
		return err
	}
	_, err := r.query().Update(ctx, entity)
	return translateError(err)
}

// UpdatePartial writes only the named fields, addressed by primary key.
func (r *Repository[T]) UpdatePartial(ctx context.Context, id any, updates map[string]any) error {
	info, err := r.info()
	if err != nil {
		return translateError(err)
	}
	keyColumn, err := singleKeyColumn(info, "UpdatePartial")
	if err != nil {
		return err
	}

	names := resolver{info: info}
	columns := make(map[string]any, len(updates))
	for field, value := range updates {
		columns[names.column(field)] = value
	}

	_, err = r.query().
		Where(orm.Eq(keyColumn, id)).
		UpdateAll(ctx, columns)
	return translateError(err)
}

// Delete removes an entity by primary key. The row is loaded first so the
// delete hooks see a real entity, and so a soft-deletable model is marked
// rather than removed.
func (r *Repository[T]) Delete(ctx context.Context, id any) error {
	entity, err := r.query().Find(ctx, id)
	if err != nil {
		return translateError(err)
	}
	_, err = r.query().Delete(ctx, entity)
	return translateError(err)
}

func (r *Repository[T]) DeleteByCondition(ctx context.Context, condition gpa.Condition) error {
	info, err := r.info()
	if err != nil {
		return translateError(err)
	}
	predicate, err := translateCondition(resolver{info: info}, condition)
	if err != nil {
		return err
	}
	if predicate.IsZero() {
		return gpa.NewError(gpa.ErrorTypeInvalidArgument,
			"gpaorm: DeleteByCondition requires a condition")
	}
	_, err = r.query().Where(predicate).DeleteAll(ctx)
	return translateError(err)
}

// =====================================
// Queries
// =====================================

func (r *Repository[T]) Query(ctx context.Context, opts ...gpa.QueryOption) ([]*T, error) {
	info, err := r.info()
	if err != nil {
		return nil, translateError(err)
	}
	query, err := buildQuery(r.query(), info, opts...)
	if err != nil {
		return nil, err
	}
	values, err := query.All(ctx)
	if err != nil {
		return nil, translateError(err)
	}
	return toPointers(ctx, values)
}

func (r *Repository[T]) QueryOne(ctx context.Context, opts ...gpa.QueryOption) (*T, error) {
	info, err := r.info()
	if err != nil {
		return nil, translateError(err)
	}
	query, err := buildQuery(r.query(), info, opts...)
	if err != nil {
		return nil, err
	}
	entity, err := query.First(ctx)
	if err != nil {
		return nil, translateError(err)
	}
	if err := runAfterFind(ctx, entity); err != nil {
		return nil, err
	}
	return entity, nil
}

func (r *Repository[T]) Count(ctx context.Context, opts ...gpa.QueryOption) (int64, error) {
	info, err := r.info()
	if err != nil {
		return 0, translateError(err)
	}
	query, err := buildQuery(r.query(), info, opts...)
	if err != nil {
		return 0, err
	}
	count, err := query.Count(ctx)
	return count, translateError(err)
}

func (r *Repository[T]) Exists(ctx context.Context, opts ...gpa.QueryOption) (bool, error) {
	info, err := r.info()
	if err != nil {
		return false, translateError(err)
	}
	query, err := buildQuery(r.query(), info, opts...)
	if err != nil {
		return false, err
	}
	exists, err := query.Exists(ctx)
	return exists, translateError(err)
}

func (r *Repository[T]) RawQuery(ctx context.Context, query string, args []any) ([]*T, error) {
	values, err := r.raw(query, args...).All(ctx)
	if err != nil {
		return nil, translateError(err)
	}
	return toPointers(ctx, values)
}

func (r *Repository[T]) RawExec(ctx context.Context, query string, args []any) (gpa.Result, error) {
	return r.exec(ctx, query, args...)
}

// =====================================
// SQLRepository
// =====================================

func (r *Repository[T]) FindBySQL(ctx context.Context, query string, args []any) ([]*T, error) {
	return r.RawQuery(ctx, query, args)
}

func (r *Repository[T]) ExecSQL(ctx context.Context, query string, args ...any) (gpa.Result, error) {
	return r.exec(ctx, query, args...)
}

// FindWithRelations eager-loads the named relations. Relation names are the
// struct field names, the same vocabulary gpa.Preload uses.
func (r *Repository[T]) FindWithRelations(ctx context.Context, relations []string, opts ...gpa.QueryOption) ([]*T, error) {
	return r.Query(ctx, append(opts, gpa.Preload(relations...))...)
}

func (r *Repository[T]) FindByIDWithRelations(ctx context.Context, id any, relations []string) (*T, error) {
	info, err := r.info()
	if err != nil {
		return nil, translateError(err)
	}
	keyColumn, err := singleKeyColumn(info, "FindByIDWithRelations")
	if err != nil {
		return nil, err
	}
	return r.QueryOne(ctx,
		gpa.Where(keyColumn, gpa.OpEqual, id),
		gpa.Preload(relations...))
}

// CreateTable is deliberately not implemented. The ORM does not generate DDL
// from struct definitions; schema is owned by the migration module, which is
// the framework's single source of truth for it.
func (r *Repository[T]) CreateTable(ctx context.Context) error {
	return unsupported("CreateTable is not implemented; define the schema with github.com/lemmego/migration")
}

func (r *Repository[T]) DropTable(ctx context.Context) error {
	info, err := r.info()
	if err != nil {
		return translateError(err)
	}
	_, err = r.exec(ctx, "DROP TABLE "+r.quote(info.Table))
	return err
}

func (r *Repository[T]) CreateIndex(ctx context.Context, fields []string, unique bool) error {
	info, err := r.info()
	if err != nil {
		return translateError(err)
	}
	if len(fields) == 0 {
		return gpa.NewError(gpa.ErrorTypeInvalidArgument, "gpaorm: CreateIndex needs at least one field")
	}

	names := resolver{info: info}
	columns := make([]string, len(fields))
	quoted := make([]string, len(fields))
	for i, field := range fields {
		columns[i] = names.column(field)
		quoted[i] = r.quote(columns[i])
	}

	statement := "CREATE INDEX "
	if unique {
		statement = "CREATE UNIQUE INDEX "
	}
	indexName := "idx_" + info.Table + "_" + strings.Join(columns, "_")
	statement += r.quote(indexName) + " ON " + r.quote(info.Table) +
		" (" + strings.Join(quoted, ", ") + ")"

	_, err = r.exec(ctx, statement)
	return err
}

func (r *Repository[T]) DropIndex(ctx context.Context, indexName string) error {
	info, err := r.info()
	if err != nil {
		return translateError(err)
	}
	statement := "DROP INDEX " + r.quote(indexName)
	// MySQL requires the table; the others reject it.
	if r.dialect().Name() == "mysql" {
		statement += " ON " + r.quote(info.Table)
	}
	_, err = r.exec(ctx, statement)
	return err
}

// =====================================
// MigratableRepository
// =====================================

func (r *Repository[T]) MigrateTable(ctx context.Context) error {
	return unsupported("MigrateTable is not implemented; use github.com/lemmego/migration")
}

func (r *Repository[T]) GetMigrationStatus(ctx context.Context) (gpa.MigrationStatus, error) {
	return gpa.MigrationStatus{}, unsupported("GetMigrationStatus is not implemented; use github.com/lemmego/migration")
}

func (r *Repository[T]) GetTableInfo(ctx context.Context) (gpa.TableInfo, error) {
	return gpa.TableInfo{}, unsupported("GetTableInfo is not implemented; introspection is not part of the ORM")
}

// =====================================
// Metadata and lifecycle
// =====================================

// GetEntityInfo exposes the ORM's metadata in GPA's shape, relations
// included.
func (r *Repository[T]) GetEntityInfo() (*gpa.EntityInfo, error) {
	info, err := r.info()
	if err != nil {
		return nil, translateError(err)
	}

	fields := make([]gpa.FieldInfo, 0, len(info.Fields))
	for _, field := range info.Fields {
		fields = append(fields, gpa.FieldInfo{
			Name:            field.Name,
			Type:            field.Type,
			Tag:             "column:" + field.Column,
			IsPrimaryKey:    field.PrimaryKey,
			IsAutoIncrement: field.AutoIncrement,
			IsNullable:      field.Type != nil && field.Type.Kind() == reflect.Pointer,
		})
	}

	primaryKey := make([]string, 0, len(info.PrimaryKeys))
	for _, key := range info.PrimaryKeys {
		primaryKey = append(primaryKey, key.Column)
	}

	relations := make([]gpa.RelationInfo, 0, len(info.Relations))
	for _, relation := range info.Relations {
		relations = append(relations, gpa.RelationInfo{
			Name:         relation.Name,
			Type:         relationType(relation.Kind),
			TargetEntity: relation.Target.Name(),
			ForeignKey:   relation.ForeignKeyField,
			References:   relation.ReferencesField,
		})
	}

	return &gpa.EntityInfo{
		Name:       info.Type.Name(),
		TableName:  info.Table,
		Fields:     fields,
		PrimaryKey: primaryKey,
		Relations:  relations,
	}, nil
}

// Close releases the repository. The connection belongs to the provider and
// is intentionally left open, since repositories are cheap and shared.
func (r *Repository[T]) Close() error { return nil }

func (r *Repository[T]) dialect() orm.Dialect { return r.db.Dialect() }

func (r *Repository[T]) quote(name string) string { return r.dialect().Quote(name) }

// singleKeyColumn resolves the one column GPA's id-addressed operations need.
//
// GPA's FindByID, Delete and UpdatePartial all take a single id, so a model
// with a composite key cannot be addressed through them. Saying that plainly
// beats reporting "no primary key", which is not what is wrong.
func singleKeyColumn(info *orm.ModelInfo, operation string) (string, error) {
	if info.PrimaryKey != nil {
		return info.PrimaryKey.Column, nil
	}
	if info.HasCompositeKey() {
		return "", gpa.NewError(gpa.ErrorTypeUnsupported, fmt.Sprintf(
			"gpaorm: %s addresses one key, but %s has a composite primary key (%s); use Query with explicit conditions",
			operation, info.Type, strings.Join(keyColumns(info), ", ")))
	}
	return "", gpa.NewError(gpa.ErrorTypeInvalidArgument,
		fmt.Sprintf("gpaorm: model %s has no primary key", info.Type))
}

func keyColumns(info *orm.ModelInfo) []string {
	columns := make([]string, len(info.PrimaryKeys))
	for i, key := range info.PrimaryKeys {
		columns[i] = key.Column
	}
	return columns
}

func relationType(kind orm.RelationKind) gpa.RelationType {
	switch kind {
	case orm.HasOne:
		return gpa.RelationOneToOne
	case orm.HasMany:
		return gpa.RelationOneToMany
	case orm.BelongsTo:
		return gpa.RelationManyToOne
	case orm.ManyToMany:
		return gpa.RelationManyToMany
	}
	return ""
}

// toPointers converts the ORM's value slices into the pointer slices GPA
// returns, running the AfterFind hook on each entity.
func toPointers[T any](ctx context.Context, values []T) ([]*T, error) {
	result := make([]*T, len(values))
	for i := range values {
		entity := &values[i]
		if err := runAfterFind(ctx, entity); err != nil {
			return nil, err
		}
		result[i] = entity
	}
	return result, nil
}

// runValidate and runAfterFind cover the hooks GPA declares that the ORM does
// not. The create, update and delete hooks are run by the ORM itself: its
// interfaces are signature-identical to GPA's, so an entity satisfying one
// satisfies the other.
func runValidate(ctx context.Context, entity any) error {
	hook, ok := entity.(gpa.ValidationHook)
	if !ok {
		return nil
	}
	if err := hook.Validate(ctx); err != nil {
		return gpa.NewErrorWithCause(gpa.ErrorTypeValidation, "validation failed", err)
	}
	return nil
}

func runAfterFind(ctx context.Context, entity any) error {
	hook, ok := entity.(gpa.AfterFindHook)
	if !ok {
		return nil
	}
	if err := hook.AfterFind(ctx); err != nil {
		return gpa.NewErrorWithCause(gpa.ErrorTypeInternal, "after-find hook failed", err)
	}
	return nil
}
