package gpaorm

import (
	"fmt"
	"reflect"

	"github.com/lemmego/gpa"
	"github.com/lemmego/orm"
)

// resolver turns the field names GPA carries at runtime into ORM columns.
// Callers may use either the struct field name or the column name, so both
// are accepted.
type resolver struct{ info *orm.ModelInfo }

func (r resolver) column(name string) string {
	if r.info != nil {
		if column, ok := r.info.Column(name); ok {
			return column
		}
	}
	return name
}

// buildQuery folds a set of GPA options onto an ORM query.
//
// Unlike a translation that quietly drops what it cannot express, anything
// unsupported is reported: a silently ignored FOR UPDATE or composite
// condition changes what the query means, and the caller needs to know.
func buildQuery[T any](query *orm.Query[T], info *orm.ModelInfo, opts ...gpa.QueryOption) (*orm.Query[T], error) {
	spec := &gpa.Query{}
	for _, opt := range opts {
		if opt != nil {
			opt.Apply(spec)
		}
	}
	names := resolver{info: info}

	for _, condition := range spec.Conditions {
		predicate, err := translateCondition(names, condition)
		if err != nil {
			return nil, err
		}
		query = query.Where(predicate)
	}

	if len(spec.Fields) > 0 {
		columns := make([]string, len(spec.Fields))
		for i, field := range spec.Fields {
			columns[i] = names.column(field)
		}
		query = query.Select(columns...)
	}

	for _, join := range spec.Joins {
		table := join.Table
		if join.Alias != "" {
			table = join.Alias
		}
		condition := orm.RawPredicate(join.Condition)
		switch join.Type {
		case gpa.JoinLeft:
			query = query.LeftJoinTable(table, condition)
		case gpa.JoinInner, "":
			query = query.JoinTable(table, condition)
		default:
			return nil, unsupported(fmt.Sprintf("%s JOIN is not implemented", join.Type))
		}
	}

	if len(spec.Groups) > 0 {
		groups := make([]string, len(spec.Groups))
		for i, group := range spec.Groups {
			groups[i] = names.column(group)
		}
		query = query.GroupBy(groups...)
	}

	for _, having := range spec.Having {
		predicate, err := translateCondition(names, having)
		if err != nil {
			return nil, err
		}
		query = query.Having(predicate)
	}

	for _, order := range spec.Orders {
		column := names.column(order.Field)
		if order.Direction == gpa.OrderDesc {
			query = query.OrderBy(orm.Desc(column))
			continue
		}
		query = query.OrderBy(orm.Asc(column))
	}

	if spec.Distinct {
		query = query.Distinct()
	}
	if spec.Limit != nil {
		query = query.Limit(*spec.Limit)
	}
	if spec.Offset != nil {
		query = query.Offset(*spec.Offset)
	}
	if len(spec.Preloads) > 0 {
		query = query.With(spec.Preloads...)
	}

	if spec.Lock != "" && spec.Lock != gpa.LockNone {
		return nil, unsupported("row locking is not implemented; a silently dropped lock would be unsafe")
	}
	if len(spec.SubQueries) > 0 {
		return nil, unsupported("subqueries are not implemented")
	}
	return query, nil
}

func translateCondition(names resolver, condition gpa.Condition) (orm.Predicate, error) {
	switch typed := condition.(type) {
	case nil:
		return orm.Predicate{}, nil

	case gpa.BasicCondition:
		return translateBasic(names, typed)

	case gpa.CompositeCondition:
		return translateComposite(names, typed)

	case gpa.SubQueryCondition:
		return orm.Predicate{}, unsupported("subquery conditions are not implemented")
	}
	return orm.Predicate{}, unsupported(fmt.Sprintf("condition type %T is not implemented", condition))
}

// translateComposite implements AND/OR/NOT grouping. GPA's own GORM provider
// drops these on the floor, which turns a narrow query into a broad one; here
// they are translated properly.
func translateComposite(names resolver, composite gpa.CompositeCondition) (orm.Predicate, error) {
	parts := make([]orm.Predicate, 0, len(composite.Conditions))
	for _, inner := range composite.Conditions {
		predicate, err := translateCondition(names, inner)
		if err != nil {
			return orm.Predicate{}, err
		}
		if !predicate.IsZero() {
			parts = append(parts, predicate)
		}
	}
	if len(parts) == 0 {
		return orm.Predicate{}, nil
	}

	switch composite.Logic {
	case gpa.LogicOr:
		return orm.Or(parts...), nil
	case gpa.LogicNot:
		return orm.Not(orm.And(parts...)), nil
	case gpa.LogicAnd, "":
		return orm.And(parts...), nil
	}
	return orm.Predicate{}, unsupported(fmt.Sprintf("logic operator %q is not implemented", composite.Logic))
}

func translateBasic(names resolver, condition gpa.BasicCondition) (orm.Predicate, error) {
	column := names.column(condition.FieldName)
	value := condition.Val

	switch condition.Op {
	case gpa.OpEqual, "":
		return orm.Eq(column, value), nil
	case gpa.OpNotEqual:
		return orm.Ne(column, value), nil
	case gpa.OpGreaterThan:
		return orm.Gt(column, value), nil
	case gpa.OpGreaterThanOrEqual:
		return orm.Gte(column, value), nil
	case gpa.OpLessThan:
		return orm.Lt(column, value), nil
	case gpa.OpLessThanOrEqual:
		return orm.Lte(column, value), nil
	case gpa.OpLike:
		return orm.Like(column, fmt.Sprint(value)), nil
	case gpa.OpNotLike:
		return orm.NotLike(column, fmt.Sprint(value)), nil
	case gpa.OpIsNull:
		return orm.IsNull(column), nil
	case gpa.OpIsNotNull:
		return orm.IsNotNull(column), nil
	case gpa.OpContains:
		return orm.Like(column, "%"+fmt.Sprint(value)+"%"), nil
	case gpa.OpStartsWith:
		return orm.Like(column, fmt.Sprint(value)+"%"), nil
	case gpa.OpEndsWith:
		return orm.Like(column, "%"+fmt.Sprint(value)), nil

	case gpa.OpIn, gpa.OpNotIn:
		values, err := expandValues(value)
		if err != nil {
			return orm.Predicate{}, err
		}
		if condition.Op == gpa.OpIn {
			return orm.In(column, values...), nil
		}
		return orm.NotIn(column, values...), nil

	case gpa.OpBetween, gpa.OpNotBetween:
		values, err := expandValues(value)
		if err != nil {
			return orm.Predicate{}, err
		}
		if len(values) != 2 {
			return orm.Predicate{}, gpa.NewError(gpa.ErrorTypeInvalidArgument,
				"gpaorm: BETWEEN needs exactly two bounds")
		}
		between := orm.Between(column, values[0], values[1])
		if condition.Op == gpa.OpBetween {
			return between, nil
		}
		return orm.Not(between), nil
	}

	return orm.Predicate{}, unsupported(fmt.Sprintf("operator %q is not implemented", condition.Op))
}

// expandValues flattens the value of an IN or BETWEEN condition, which may
// arrive as []interface{} or as any concrete slice type.
func expandValues(value any) ([]any, error) {
	if value == nil {
		return nil, nil
	}
	if values, ok := value.([]any); ok {
		return values, nil
	}

	reflected := reflect.ValueOf(value)
	if reflected.Kind() != reflect.Slice && reflected.Kind() != reflect.Array {
		return []any{value}, nil
	}
	values := make([]any, reflected.Len())
	for i := range values {
		values[i] = reflected.Index(i).Interface()
	}
	return values, nil
}
