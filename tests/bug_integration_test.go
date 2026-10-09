//go:build wasm

package tests_test

import (
	"testing"

	"webtyp.com/indexdb"
	. "webtyp.com/model"
	"webtyp.com/storage"
)

// SimpleUser implements the Model interface for testing
type SimpleUser struct {
	ID    string `db:"pk"`
	Email string `db:"unique"`
}

func (m *SimpleUser) ModelName() string { return "simple_users" }
func (m *SimpleUser) Schema() []Field {
	return []Field{
		{Name: "ID", Type: Text(), DB: &FieldDB{PK: true}},
		{Name: "Email", Type: Text(), DB: &FieldDB{Unique: true}},
	}
}
func (m *SimpleUser) Pointers() []any             { return []any{&m.ID, &m.Email} }
func (m *SimpleUser) EncodeFields(wr FieldWriter) {}
func (m *SimpleUser) DecodeFields(r FieldReader)  {}
func (m *SimpleUser) IsNil() bool                 { return m == nil }

// SimpleSession implements the Model interface for testing
type SimpleSession struct {
	ID     string `db:"pk"`
	UserID string `db:"ref=simple_users"`
}

func (m *SimpleSession) ModelName() string { return "simple_sessions" }
func (m *SimpleSession) Schema() []Field {
	return []Field{
		{Name: "ID", Type: Text(), DB: &FieldDB{PK: true}},
		{Name: "UserID", Type: Text()},
	}
}
func (m *SimpleSession) Pointers() []any             { return []any{&m.ID, &m.UserID} }
func (m *SimpleSession) EncodeFields(wr FieldWriter) {}
func (m *SimpleSession) DecodeFields(r FieldReader)  {}
func (m *SimpleSession) IsNil() bool                 { return m == nil }

// NumericPK implements the Model interface for testing
type NumericPK struct {
	ID    int64 `db:"pk"`
	Value string
}

func (m *NumericPK) ModelName() string { return "numeric_pks" }
func (m *NumericPK) Schema() []Field {
	return []Field{
		{Name: "ID", Type: Int(), DB: &FieldDB{PK: true}},
		{Name: "Value", Type: Text()},
	}
}
func (m *NumericPK) Pointers() []any             { return []any{&m.ID, &m.Value} }
func (m *NumericPK) EncodeFields(wr FieldWriter) {}
func (m *NumericPK) DecodeFields(r FieldReader)  {}
func (m *NumericPK) IsNil() bool                 { return m == nil }

func TestBugScenario(t *testing.T) {
	t.Run("MultipleInitialization", func(t *testing.T) {
		dbName := "multi_init_test"
		db1, err := indexdb.New(dbName, &SimpleUser{})
		if err != nil {
			t.Fatal(err)
		}
		_ = db1

		// Initialize again
		db2, err := indexdb.New(dbName, &SimpleUser{})
		if err != nil {
			t.Fatal("Second initialization failed", err)
		}
		_ = db2
	})

	t.Run("WaitForSuccess", func(t *testing.T) {
		dbName := "wait_success_test"
		// This test is implicit because New blocks until initDone is closed.
		// If it doesn't block, subsequent operations would fail.
		db, err := indexdb.New(dbName, &SimpleUser{})
		if err != nil {
			t.Fatal(err)
		}

		user := SimpleUser{ID: "u1", Email: "u1@test.com"}
		query := storage.Query{
			Action:  storage.ActionCreate,
			Table:   "simple_users",
			Columns: []string{"ID", "Email"},
			Values:  []any{user.ID, user.Email},
		}
		err = db.Exec("", query, &user)
		if err != nil {
			t.Fatalf("Operation immediately after InitDB failed: %v", err)
		}
	})

	t.Run("TextPK", func(t *testing.T) {
		db := SetupDB(t, "text_pk_test", &SimpleUser{})
		u := SimpleUser{ID: "alice", Email: "alice@test.com"}

		query := storage.Query{
			Action:  storage.ActionCreate,
			Table:   "simple_users",
			Columns: []string{"ID", "Email"},
			Values:  []any{u.ID, u.Email},
		}
		if err := db.Exec("", query, &u); err != nil {
			t.Fatalf("Create failed: %v", err)
		}

		var read SimpleUser
		readQuery := storage.Query{
			Action:     storage.ActionReadOne,
			Table:      "simple_users",
			Conditions: []storage.Condition{storage.Eq("ID", "alice")},
		}
		err := db.QueryRow("", readQuery, &read).Scan()
		if err != nil {
			t.Fatalf("ReadOne failed: %v", err)
		}
		if read.Email != u.Email {
			t.Errorf("Expected email %s, got %s", u.Email, read.Email)
		}
	})

	t.Run("NumericPK", func(t *testing.T) {
		db := SetupDB(t, "numeric_pk_test", &NumericPK{})
		n := NumericPK{ID: 123, Value: "test"}

		query := storage.Query{
			Action:  storage.ActionCreate,
			Table:   "numeric_pks",
			Columns: []string{"ID", "Value"},
			Values:  []any{n.ID, n.Value},
		}
		if err := db.Exec("", query, &n); err != nil {
			t.Fatalf("Create failed: %v", err)
		}

		var read NumericPK
		readQuery := storage.Query{
			Action:     storage.ActionReadOne,
			Table:      "numeric_pks",
			Conditions: []storage.Condition{storage.Eq("ID", int64(123))},
		}
		err := db.QueryRow("", readQuery, &read).Scan()
		if err != nil {
			t.Fatalf("ReadOne failed: %v", err)
		}
		if read.ID != n.ID {
			t.Errorf("Expected ID %d, got %d", n.ID, read.ID)
		}
	})

	t.Run("TableNotFound", func(t *testing.T) {
		db := SetupDB(t, "table_not_found_test", &SimpleUser{})

		var session SimpleSession
		readQuery := storage.Query{
			Action:     storage.ActionReadOne,
			Table:      "simple_sessions",
			Conditions: []storage.Condition{storage.Eq("ID", "s1")},
		}
		err := db.QueryRow("", readQuery, &session).Scan()
		if err == nil {
			t.Fatal("Expected error for non-existent table, got nil")
		}
	})

	t.Run("CursorConcurrency", func(t *testing.T) {
		db := SetupDB(t, "concurrency_test", &SimpleUser{})

		// Seed some data
		for i := 0; i < 10; i++ {
			u := SimpleUser{ID: string(rune('0' + i)), Email: "test@test.com"}
			query := storage.Query{
				Action:  storage.ActionCreate,
				Table:   "simple_users",
				Columns: []string{"ID", "Email"},
				Values:  []any{u.ID, u.Email},
			}
			_ = db.Exec("", query, &u)
		}

		// Sequential rapid queries
		for i := 0; i < 5; i++ {
			var u SimpleUser
			readQuery := storage.Query{
				Action:     storage.ActionReadOne,
				Table:      "simple_users",
				Conditions: []storage.Condition{storage.Eq("ID", "0")},
			}
			err := db.QueryRow("", readQuery, &u).Scan()
			if err != nil {
				t.Fatalf("Query %d failed: %v", i, err)
			}
		}
	})

	t.Run("EmptyResult", func(t *testing.T) {
		db := SetupDB(t, "empty_result_test", &SimpleUser{})

		var u SimpleUser
		readQuery := storage.Query{
			Action:     storage.ActionReadOne,
			Table:      "simple_users",
			Conditions: []storage.Condition{storage.Eq("ID", "missing")},
		}
		err := db.QueryRow("", readQuery, &u).Scan()
		if err == nil {
			t.Fatal("Expected error for missing record, got nil")
		}
		if err != storage.ErrNoRows {
			t.Errorf("Expected storage.ErrNoRows, got '%v'", err)
		}
	})
}
