// Package gpaorm implements the GPA persistence contracts on top of the
// Lemmego ORM.
//
// The dependency direction is gpaorm -> orm + gpa. The ORM itself never
// imports GPA, so the translation lives entirely here.
package gpaorm

import (
	"errors"

	"github.com/lemmego/gpa"
	"github.com/lemmego/orm"
)

// translateError maps ORM errors onto the GPA error vocabulary, preserving the
// original as the cause so callers can still reach the driver error.
func translateError(err error) error {
	if err == nil {
		return nil
	}

	// Already translated.
	var alreadyGPA gpa.GPAError
	if errors.As(err, &alreadyGPA) {
		return err
	}

	switch {
	case errors.Is(err, orm.ErrNotFound):
		return gpa.NewErrorWithCause(gpa.ErrorTypeNotFound, "record not found", err)
	case errors.Is(err, orm.ErrUnsupported):
		return gpa.NewErrorWithCause(gpa.ErrorTypeUnsupported, err.Error(), err)
	case errors.Is(err, orm.ErrUnsafeMutation):
		return gpa.NewErrorWithCause(gpa.ErrorTypeInvalidArgument, "mutation requires a condition", err)
	case errors.Is(err, orm.ErrClosed):
		return gpa.NewErrorWithCause(gpa.ErrorTypeTransaction, "transaction is closed", err)
	}

	// A unique violation is a duplicate; every other constraint keeps its own
	// category rather than being flattened into a generic database error.
	var constraint *orm.ConstraintError
	if errors.As(err, &constraint) {
		if constraint.Kind == orm.ConstraintUnique {
			return gpa.NewErrorWithCause(gpa.ErrorTypeDuplicate, constraint.Error(), err)
		}
		return gpa.NewErrorWithCause(gpa.ErrorTypeConstraint, constraint.Error(), err)
	}

	var mapping *orm.MappingError
	if errors.As(err, &mapping) {
		return gpa.NewErrorWithCause(gpa.ErrorTypeInternal, mapping.Error(), err)
	}

	return gpa.NewErrorWithCause(gpa.ErrorTypeDatabase, err.Error(), err)
}

func unsupported(feature string) error {
	return gpa.NewError(gpa.ErrorTypeUnsupported, "gpaorm: "+feature)
}
