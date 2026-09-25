package gpaorm

import (
	"context"
	"errors"

	"github.com/lemmego/gpa"
	"github.com/lemmego/orm"
)

// errRolledBack signals that the caller asked for a rollback through the GPA
// API. It never escapes Transaction.
var errRolledBack = errors.New("gpaorm: rolled back by the caller")

// Transaction adapts an ORM transaction to gpa.Transaction[T].
//
// GPA models a transaction as a per-entity-type repository, while the ORM's
// transaction is deliberately entity-agnostic so one unit of work can span
// several models. The bridge is that many Transaction[T] values can share a
// single *orm.Tx.
type Transaction[T any] struct {
	*Repository[T]
	rolledBack bool
}

// Transaction runs fn inside a database transaction, committing when it
// returns nil and rolling back on error or panic.
func (r *Repository[T]) Transaction(ctx context.Context, fn gpa.TransactionFunc[T]) error {
	// Already inside one: reuse it rather than nesting, which GPA does not
	// define and not every database supports.
	if r.tx != nil {
		return fn(&Transaction[T]{Repository: r})
	}

	err := r.db.Transaction(ctx, func(tx *orm.Tx) error {
		scoped := &Transaction[T]{
			Repository: &Repository[T]{provider: r.provider, db: r.db, tx: tx},
		}
		if err := fn(scoped); err != nil {
			return err
		}
		if scoped.rolledBack {
			return errRolledBack
		}
		return nil
	})

	// An explicit Rollback is what the caller asked for, not a failure.
	if errors.Is(err, errRolledBack) {
		return nil
	}
	return translateError(err)
}

// TxFor derives a repository for another entity type from an existing
// transaction, which is how one unit of work spans several models despite
// gpa.Transaction being parameterised by a single type.
func TxFor[U any, T any](tx *Transaction[T]) *Repository[U] {
	return &Repository[U]{provider: tx.provider, db: tx.db, tx: tx.tx}
}

// Commit is a no-op: the enclosing Transaction commits when fn returns nil.
// It exists to satisfy gpa.Transaction.
func (t *Transaction[T]) Commit() error { return nil }

// Rollback marks the transaction for rollback. The actual rollback happens
// when control returns to Transaction, so it stays in one place.
func (t *Transaction[T]) Rollback() error {
	t.rolledBack = true
	return nil
}

// SetSavepoint and RollbackToSavepoint delegate to the ORM, which owns the
// savepoint SQL and the identifier validation that has to go with it.
func (t *Transaction[T]) SetSavepoint(name string) error {
	return t.savepoint(func(tx *orm.Tx) error {
		return tx.Savepoint(context.Background(), name)
	})
}

func (t *Transaction[T]) RollbackToSavepoint(name string) error {
	return t.savepoint(func(tx *orm.Tx) error {
		return tx.RollbackTo(context.Background(), name)
	})
}

func (t *Transaction[T]) savepoint(fn func(*orm.Tx) error) error {
	if t.tx == nil {
		return gpa.NewError(gpa.ErrorTypeTransaction, "gpaorm: no active transaction")
	}
	err := fn(t.tx)
	if errors.Is(err, orm.ErrInvalidIdentifier) {
		return gpa.NewErrorWithCause(gpa.ErrorTypeInvalidArgument, err.Error(), err)
	}
	return translateError(err)
}
