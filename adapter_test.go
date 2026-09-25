package gpaorm

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/lemmego/gpa"
	"github.com/lemmego/orm"
	_ "github.com/mattn/go-sqlite3"
)

type User struct {
	ID    int    `orm:"primaryKey;autoIncrement"`
	Email string `orm:"column:email_address"`
	Age   int
	Posts []Post `orm:"hasMany:UserID"`
}

type Post struct {
	ID     int `orm:"primaryKey;autoIncrement"`
	UserID int
	Title  string
}

type Note struct {
	ID        int `orm:"primaryKey;autoIncrement"`
	Body      string
	DeletedAt *time.Time
}

// Validated exercises the GPA-only ValidationHook.
type Validated struct {
	ID    int `orm:"primaryKey;autoIncrement"`
	Email string
}

func (v *Validated) Validate(context.Context) error {
	if v.Email == "" {
		return errors.New("email is required")
	}
	return nil
}

const schema = `
CREATE TABLE users (id INTEGER PRIMARY KEY AUTOINCREMENT, email_address TEXT, age INTEGER);
CREATE TABLE posts (id INTEGER PRIMARY KEY AUTOINCREMENT, user_id INTEGER, title TEXT);
CREATE TABLE notes (id INTEGER PRIMARY KEY AUTOINCREMENT, body TEXT, deleted_at DATETIME);
CREATE TABLE validateds (id INTEGER PRIMARY KEY AUTOINCREMENT, email TEXT);
`

func newProvider(t *testing.T) *Provider {
	t.Helper()
	sqlDB, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { sqlDB.Close() })

	if _, err := sqlDB.Exec(schema); err != nil {
		t.Fatal(err)
	}
	return New(orm.Open(sqlDB, orm.SQLite()))
}

func seedUsers(t *testing.T, repo gpa.Repository[User], count int) []*User {
	t.Helper()
	ctx := context.Background()
	users := make([]*User, 0, count)
	for i := range count {
		user := &User{Email: fmt.Sprintf("user%d@example.com", i), Age: 20 + i}
		if err := repo.Create(ctx, user); err != nil {
			t.Fatal(err)
		}
		users = append(users, user)
	}
	return users
}

func TestCRUDThroughGPA(t *testing.T) {
	provider := newProvider(t)
	repo := GetRepository[User](provider)
	ctx := context.Background()

	user := &User{Email: "crud@example.com", Age: 30}
	if err := repo.Create(ctx, user); err != nil {
		t.Fatal(err)
	}
	if user.ID == 0 {
		t.Fatal("expected the generated key to reach the entity through the adapter")
	}

	found, err := repo.FindByID(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if found.Email != "crud@example.com" {
		t.Fatalf("unexpected entity: %#v", found)
	}

	found.Age = 31
	if err := repo.Update(ctx, found); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdatePartial(ctx, user.ID, map[string]any{"Email": "partial@example.com"}); err != nil {
		t.Fatal(err)
	}
	reloaded, err := repo.FindByID(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Email != "partial@example.com" || reloaded.Age != 31 {
		t.Fatalf("unexpected entity after updates: %#v", reloaded)
	}

	if err := repo.Delete(ctx, user.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.FindByID(ctx, user.ID); !gpa.IsNotFound(err) {
		t.Fatalf("expected a GPA not-found error, got %v", err)
	}
}

func TestCreateBatchAndCount(t *testing.T) {
	repo := GetRepository[User](newProvider(t))
	ctx := context.Background()

	if err := repo.CreateBatch(ctx, []*User{
		{Email: "a@example.com", Age: 20},
		{Email: "b@example.com", Age: 30},
	}); err != nil {
		t.Fatal(err)
	}
	count, err := repo.Count(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("expected 2 rows, got %d", count)
	}

	exists, err := repo.Exists(ctx, gpa.Where("age", gpa.OpGreaterThan, 25))
	if err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Fatal("expected a match")
	}
}

func TestQueryOptionTranslation(t *testing.T) {
	repo := GetRepository[User](newProvider(t))
	ctx := context.Background()
	seedUsers(t, repo, 5) // ages 20..24

	users, err := repo.Query(ctx,
		gpa.Where("age", gpa.OpGreaterThanOrEqual, 22),
		gpa.OrderBy("age", gpa.OrderDesc),
		gpa.Limit(2),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 2 || users[0].Age != 24 {
		t.Fatalf("unexpected result: %#v", users)
	}

	inList, err := repo.Query(ctx, gpa.WhereIn("age", []any{20, 24}))
	if err != nil {
		t.Fatal(err)
	}
	if len(inList) != 2 {
		t.Fatalf("expected 2 from IN, got %d", len(inList))
	}

	between, err := repo.Query(ctx, gpa.Where("age", gpa.OpBetween, []any{21, 23}))
	if err != nil {
		t.Fatal(err)
	}
	if len(between) != 3 {
		t.Fatalf("expected 3 from BETWEEN, got %d", len(between))
	}

	like, err := repo.Query(ctx, gpa.Where("email_address", gpa.OpStartsWith, "user1"))
	if err != nil {
		t.Fatal(err)
	}
	if len(like) != 1 {
		t.Fatalf("expected 1 from STARTS_WITH, got %d", len(like))
	}

	// Field selection maps struct field names as well as column names.
	projected, err := repo.Query(ctx, gpa.Fields("Email"), gpa.Limit(1))
	if err != nil {
		t.Fatal(err)
	}
	if len(projected) != 1 || projected[0].Email == "" || projected[0].Age != 0 {
		t.Fatalf("expected only the email column to be read: %#v", projected[0])
	}
}

// GPA's GORM provider silently discards composite conditions, which widens the
// query instead of narrowing it. The adapter translates them.
func TestCompositeConditionsAreTranslated(t *testing.T) {
	repo := GetRepository[User](newProvider(t))
	ctx := context.Background()
	seedUsers(t, repo, 5) // ages 20..24

	users, err := repo.Query(ctx, gpa.Or(
		gpa.WhereCondition("age", gpa.OpEqual, 20),
		gpa.WhereCondition("age", gpa.OpEqual, 24),
	))
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 2 {
		t.Fatalf("expected OR to match exactly 2 rows, got %d", len(users))
	}

	narrowed, err := repo.Query(ctx, gpa.And(
		gpa.WhereCondition("age", gpa.OpGreaterThan, 20),
		gpa.WhereCondition("age", gpa.OpLessThan, 23),
	))
	if err != nil {
		t.Fatal(err)
	}
	if len(narrowed) != 2 {
		t.Fatalf("expected AND to match 2 rows, got %d", len(narrowed))
	}
}

// Anything the adapter cannot express is reported rather than ignored: a
// dropped lock or subquery changes what the query means.
func TestUnsupportedOptionsAreReported(t *testing.T) {
	repo := GetRepository[User](newProvider(t))
	ctx := context.Background()

	if _, err := repo.Query(ctx, gpa.Lock(gpa.LockForUpdate)); !gpa.IsErrorType(err, gpa.ErrorTypeUnsupported) {
		t.Fatalf("expected an unsupported error for row locking, got %v", err)
	}
	if _, err := repo.Query(ctx, gpa.ExistsSubQuery(gpa.NewQuery())); !gpa.IsErrorType(err, gpa.ErrorTypeUnsupported) {
		t.Fatalf("expected an unsupported error for subqueries, got %v", err)
	}
	if err := repo.CreateTable(ctx); !gpa.IsErrorType(err, gpa.ErrorTypeUnsupported) {
		t.Fatalf("expected CreateTable to report unsupported, got %v", err)
	}
}

func TestPreloadRelations(t *testing.T) {
	provider := newProvider(t)
	users := GetRepository[User](provider)
	posts := GetRepository[Post](provider)
	ctx := context.Background()

	seeded := seedUsers(t, users, 3)
	for _, user := range seeded {
		for i := range 2 {
			if err := posts.Create(ctx, &Post{UserID: user.ID, Title: fmt.Sprintf("post %d", i)}); err != nil {
				t.Fatal(err)
			}
		}
	}

	loaded, err := users.FindWithRelations(ctx, []string{"Posts"})
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 3 {
		t.Fatalf("expected 3 users, got %d", len(loaded))
	}
	for _, user := range loaded {
		if len(user.Posts) != 2 {
			t.Fatalf("user %d has %d posts, want 2", user.ID, len(user.Posts))
		}
	}

	one, err := users.FindByIDWithRelations(ctx, seeded[0].ID, []string{"Posts"})
	if err != nil {
		t.Fatal(err)
	}
	if len(one.Posts) != 2 {
		t.Fatalf("expected the relation to load: %#v", one.Posts)
	}
}

func TestTransactionCommitAndRollback(t *testing.T) {
	repo := GetRepository[User](newProvider(t))
	ctx := context.Background()

	if err := repo.Transaction(ctx, func(tx gpa.Transaction[User]) error {
		return tx.Create(ctx, &User{Email: "committed@example.com"})
	}); err != nil {
		t.Fatal(err)
	}
	count, _ := repo.Count(ctx)
	if count != 1 {
		t.Fatalf("expected the transaction to commit, got %d rows", count)
	}

	wanted := errors.New("abort")
	err := repo.Transaction(ctx, func(tx gpa.Transaction[User]) error {
		if err := tx.Create(ctx, &User{Email: "discarded@example.com"}); err != nil {
			return err
		}
		return wanted
	})
	if err == nil {
		t.Fatal("expected the error to propagate")
	}
	count, _ = repo.Count(ctx)
	if count != 1 {
		t.Fatalf("expected the failed transaction to roll back, got %d rows", count)
	}
}

// An explicit Rollback through the GPA API must discard the work without
// being reported as a failure.
func TestExplicitRollback(t *testing.T) {
	repo := GetRepository[User](newProvider(t))
	ctx := context.Background()

	if err := repo.Transaction(ctx, func(tx gpa.Transaction[User]) error {
		if err := tx.Create(ctx, &User{Email: "rolled@example.com"}); err != nil {
			return err
		}
		return tx.Rollback()
	}); err != nil {
		t.Fatalf("an explicit rollback is not a failure: %v", err)
	}
	count, _ := repo.Count(ctx)
	if count != 0 {
		t.Fatalf("expected the work to be discarded, got %d rows", count)
	}
}

func TestSavepoints(t *testing.T) {
	repo := GetRepository[User](newProvider(t))
	ctx := context.Background()

	if err := repo.Transaction(ctx, func(tx gpa.Transaction[User]) error {
		if err := tx.Create(ctx, &User{Email: "keep@example.com"}); err != nil {
			return err
		}
		if err := tx.SetSavepoint("sp1"); err != nil {
			return err
		}
		if err := tx.Create(ctx, &User{Email: "undo@example.com"}); err != nil {
			return err
		}
		return tx.RollbackToSavepoint("sp1")
	}); err != nil {
		t.Fatal(err)
	}

	users, err := repo.Query(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 1 || users[0].Email != "keep@example.com" {
		t.Fatalf("expected only the pre-savepoint row: %#v", users)
	}
}

// A savepoint name is interpolated into SQL rather than bound, so it must be
// validated.
func TestSavepointNameIsValidated(t *testing.T) {
	repo := GetRepository[User](newProvider(t))
	ctx := context.Background()

	err := repo.Transaction(ctx, func(tx gpa.Transaction[User]) error {
		return tx.SetSavepoint("sp1; DROP TABLE users")
	})
	if !gpa.IsErrorType(err, gpa.ErrorTypeInvalidArgument) {
		t.Fatalf("expected the name to be rejected, got %v", err)
	}
	if _, err := repo.Count(ctx); err != nil {
		t.Fatalf("the users table should still exist: %v", err)
	}
}

// One unit of work spanning several models, despite gpa.Transaction being
// parameterised by a single entity type.
func TestTransactionSpansEntityTypes(t *testing.T) {
	provider := newProvider(t)
	users := GetRepository[User](provider)
	ctx := context.Background()

	err := users.Transaction(ctx, func(tx gpa.Transaction[User]) error {
		user := &User{Email: "owner@example.com"}
		if err := tx.Create(ctx, user); err != nil {
			return err
		}
		posts := TxFor[Post](tx.(*Transaction[User]))
		return posts.Create(ctx, &Post{UserID: user.ID, Title: "in the same tx"})
	})
	if err != nil {
		t.Fatal(err)
	}

	postCount, err := GetRepository[Post](provider).Count(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if postCount != 1 {
		t.Fatalf("expected the post to be committed alongside the user, got %d", postCount)
	}
}

func TestDuplicateBecomesGPADuplicate(t *testing.T) {
	provider := newProvider(t)
	repo := GetRepository[User](provider)
	ctx := context.Background()

	if err := repo.CreateIndex(ctx, []string{"Email"}, true); err != nil {
		t.Fatal(err)
	}
	if err := repo.Create(ctx, &User{Email: "dup@example.com"}); err != nil {
		t.Fatal(err)
	}

	err := repo.Create(ctx, &User{Email: "dup@example.com"})
	if !gpa.IsDuplicate(err) {
		t.Fatalf("expected a GPA duplicate error, got %v", err)
	}
	// The underlying ORM error stays reachable.
	var constraint *orm.ConstraintError
	if !errors.As(err, &constraint) {
		t.Fatalf("expected the ORM constraint error to remain unwrappable: %v", err)
	}
}

func TestValidationHookRuns(t *testing.T) {
	repo := GetRepository[Validated](newProvider(t))
	ctx := context.Background()

	err := repo.Create(ctx, &Validated{})
	if !gpa.IsValidation(err) {
		t.Fatalf("expected a validation error, got %v", err)
	}
	if err := repo.Create(ctx, &Validated{Email: "ok@example.com"}); err != nil {
		t.Fatal(err)
	}
}

// Soft deletes keep working through the GPA surface.
func TestSoftDeleteThroughGPA(t *testing.T) {
	repo := GetRepository[Note](newProvider(t))
	ctx := context.Background()

	note := &Note{Body: "keep the row"}
	if err := repo.Create(ctx, note); err != nil {
		t.Fatal(err)
	}
	if err := repo.Delete(ctx, note.ID); err != nil {
		t.Fatal(err)
	}
	count, err := repo.Count(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("expected the note to be hidden, got %d", count)
	}

	var raw int
	if err := repo.(*Repository[Note]).db.SQLDB().QueryRow(`SELECT COUNT(*) FROM notes`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if raw != 1 {
		t.Fatalf("expected the row to survive as soft-deleted, found %d", raw)
	}
}

func TestGetEntityInfoIncludesRelations(t *testing.T) {
	repo := GetRepository[User](newProvider(t))

	info, err := repo.GetEntityInfo()
	if err != nil {
		t.Fatal(err)
	}
	if info.Name != "User" || info.TableName != "users" {
		t.Fatalf("unexpected entity info: %#v", info)
	}
	if len(info.PrimaryKey) != 1 || info.PrimaryKey[0] != "id" {
		t.Fatalf("unexpected primary key: %#v", info.PrimaryKey)
	}
	if len(info.Fields) != 3 {
		t.Fatalf("unexpected fields: %#v", info.Fields)
	}
	// GPA's GORM provider leaves Relations nil; this one fills it in.
	if len(info.Relations) != 1 {
		t.Fatalf("expected the Posts relation to be reported: %#v", info.Relations)
	}
	if info.Relations[0].Type != gpa.RelationOneToMany || info.Relations[0].TargetEntity != "Post" {
		t.Fatalf("unexpected relation: %#v", info.Relations[0])
	}
}

func TestProviderRegistryRoundTrip(t *testing.T) {
	provider := newProvider(t)
	gpa.Register[*Provider]("gpaorm_test_instance", provider)

	resolved, err := gpa.Get[*Provider]("gpaorm_test_instance")
	if err != nil {
		t.Fatal(err)
	}
	if resolved != provider {
		t.Fatal("expected the same provider back from the registry")
	}
	if resolved.ProviderInfo().Name != ProviderName {
		t.Fatalf("unexpected provider name: %s", resolved.ProviderInfo().Name)
	}

	repo := GetRepositoryByName[User]("gpaorm_test_instance")
	if _, err := repo.Count(context.Background()); err != nil {
		t.Fatal(err)
	}

	if err := provider.Health(); err != nil {
		t.Fatal(err)
	}
	if err := provider.Configure(gpa.Config{MaxOpenConns: 1, MaxIdleConns: 1}); err != nil {
		t.Fatal(err)
	}
}

func TestRawSQLPaths(t *testing.T) {
	provider := newProvider(t)
	repo := GetRepository[User](provider)
	ctx := context.Background()
	seedUsers(t, repo, 3)

	users, err := repo.FindBySQL(ctx, `SELECT id, email_address, age FROM users WHERE age > ?`, []any{20})
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(users))
	}

	result, err := repo.ExecSQL(ctx, `UPDATE users SET age = ? WHERE age > ?`, 99, 20)
	if err != nil {
		t.Fatal(err)
	}
	if rows, _ := result.RowsAffected(); rows != 2 {
		t.Fatalf("expected 2 rows affected, got %d", rows)
	}
}

func TestDeleteByCondition(t *testing.T) {
	repo := GetRepository[User](newProvider(t))
	ctx := context.Background()
	seedUsers(t, repo, 4) // ages 20..23

	if err := repo.DeleteByCondition(ctx, gpa.WhereCondition("age", gpa.OpLessThan, 22)); err != nil {
		t.Fatal(err)
	}
	count, err := repo.Count(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("expected 2 rows left, got %d", count)
	}
}

// A model keyed on several columns cannot be addressed by GPA's single-id
// operations, and should say so rather than claiming it has no key.
type Enrollment struct {
	StudentID int `orm:"primaryKey"`
	CourseID  int `orm:"primaryKey"`
	Grade     string
}

func TestCompositeKeyIsReportedClearly(t *testing.T) {
	provider := newProvider(t)
	if _, err := provider.ORM().SQLDB().Exec(
		`CREATE TABLE enrollments (student_id INTEGER, course_id INTEGER, grade TEXT, PRIMARY KEY (student_id, course_id))`,
	); err != nil {
		t.Fatal(err)
	}
	repo := GetRepository[Enrollment](provider)
	ctx := context.Background()

	if err := repo.Create(ctx, &Enrollment{StudentID: 1, CourseID: 2, Grade: "A"}); err != nil {
		t.Fatal(err)
	}

	err := repo.UpdatePartial(ctx, 1, map[string]any{"Grade": "B"})
	if !gpa.IsErrorType(err, gpa.ErrorTypeUnsupported) {
		t.Fatalf("expected an unsupported error, got %v", err)
	}
	if !strings.Contains(err.Error(), "composite primary key") {
		t.Fatalf("the error should explain why: %v", err)
	}

	// Querying with explicit conditions still works.
	rows, err := repo.Query(ctx, gpa.And(
		gpa.WhereCondition("student_id", gpa.OpEqual, 1),
		gpa.WhereCondition("course_id", gpa.OpEqual, 2),
	))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Grade != "A" {
		t.Fatalf("unexpected rows: %#v", rows)
	}

	// Both key columns are reported in the entity metadata.
	info, err := repo.GetEntityInfo()
	if err != nil {
		t.Fatal(err)
	}
	if len(info.PrimaryKey) != 2 {
		t.Fatalf("expected both key columns, got %#v", info.PrimaryKey)
	}
}
