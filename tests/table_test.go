//go:build wasm

package tests_test

import (
	"testing"

	. "webtyp.com/model"
	"webtyp.com/storage"
)

// TestIndexDBCrudOperations tests basic CRUD operations in IndexDB
func TestIndexDBCrudOperations(t *testing.T) {

	// Setup the database with tables
	db := SetupDB(t, "crud_operations_test", &User{}, &Product{})

	userOne := User{Name: "Alice", Email: "alice@example.com"}
	userOne.ID = "1" // Manually assigning simple ID for test

	query := storage.Query{
		Action:  storage.ActionCreate,
		Table:   "user",
		Columns: []string{"ID", "Name", "Email"},
		Values:  []any{userOne.ID, userOne.Name, userOne.Email},
	}
	err := db.Exec("", query, &userOne)
	if err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}

	if userOne.ID == "" {
		t.Fatal("User ID should be generated")
	}

	// READ ONE
	var userRead User
	readQuery := storage.Query{
		Action:     storage.ActionReadOne,
		Table:      "user",
		Conditions: []storage.Condition{storage.Eq("ID", userOne.ID)},
	}
	err = db.QueryRow("", readQuery, &userRead).Scan()
	if err != nil {
		t.Fatalf("Failed to read user: %v", err)
	}
	if userRead.Name != userOne.Name {
		t.Fatalf("Expected name %s, got %s", userOne.Name, userRead.Name)
	}

	// UPDATE user
	userOne.Email = "alice@newdomain.com"
	updateQuery := storage.Query{
		Action:     storage.ActionUpdate,
		Table:      "user",
		Columns:    []string{"ID", "Name", "Email"},
		Values:     []any{userOne.ID, userOne.Name, userOne.Email},
		Conditions: []storage.Condition{storage.Eq("ID", userOne.ID)},
	}
	err = db.Exec("", updateQuery, &userOne)
	if err != nil {
		t.Fatalf("Failed to update user: %v", err)
	}

	// Verify Update
	var userUpdated User
	err = db.QueryRow("", readQuery, &userUpdated).Scan()
	if err != nil {
		t.Fatalf("Failed to read updated user: %v", err)
	}
	if userUpdated.Email != "alice@newdomain.com" {
		t.Fatalf("Expected email alice@newdomain.com, got %s", userUpdated.Email)
	}

	// DELETE
	deleteQuery := storage.Query{
		Action:     storage.ActionDelete,
		Table:      "user",
		Conditions: []storage.Condition{storage.Eq("ID", userOne.ID)},
	}
	err = db.Exec("", deleteQuery, &userOne)
	if err != nil {
		t.Fatalf("Failed to delete user: %v", err)
	}

	// Verify Delete
	var userDeleted User
	err = db.QueryRow("", readQuery, &userDeleted).Scan()
	if err == nil {
		t.Fatal("Expected error finding deleted user, got nil")
	}
	if err != storage.ErrNoRows {
		t.Fatalf("Expected ErrNoRows, got %v", err)
	}

	// Delete on non-existent
	nonExistentDelete := storage.Query{
		Action:     storage.ActionDelete,
		Table:      "user",
		Conditions: []storage.Condition{storage.Eq("ID", "999999")},
	}
	_ = db.Exec("", nonExistentDelete, &User{ID: "999999"})

	// Delete multi-condition (now fully supported!)
	multiDeleteQuery := storage.Query{
		Action:     storage.ActionDelete,
		Table:      "user",
		Conditions: []storage.Condition{storage.Eq("ID", "1"), storage.Eq("Name", "Alice")},
	}
	err = db.Exec("", multiDeleteQuery, &User{ID: "1"})
	if err != nil {
		t.Fatalf("Unexpected error finding multi-condition delete: %v", err)
	}

	// ReadOne via cursor path (no single PK condition)
	var alice User
	cursorReadQuery := storage.Query{
		Action:     storage.ActionReadOne,
		Table:      "user",
		Conditions: []storage.Condition{storage.Eq("Name", "Alice")},
	}
	_ = db.QueryRow("", cursorReadQuery, &alice).Scan()

	// Create and read by non-PK to find it
	bob := User{ID: "bob_id", Name: "Bob", Email: "bob@domain.com"}
	createBobQuery := storage.Query{
		Action:  storage.ActionCreate,
		Table:   "user",
		Columns: []string{"ID", "Name", "Email"},
		Values:  []any{bob.ID, bob.Name, bob.Email},
	}
	err = db.Exec("", createBobQuery, &bob)
	if err != nil {
		t.Fatalf("Failed to create Bob: %v", err)
	}

	var bobRead User
	readBobByNameQuery := storage.Query{
		Action:     storage.ActionReadOne,
		Table:      "user",
		Conditions: []storage.Condition{storage.Eq("Name", "Bob")},
	}
	err = db.QueryRow("", readBobByNameQuery, &bobRead).Scan()
	if err != nil {
		t.Fatalf("Failed to read Bob by name: %v", err)
	}
	if bobRead.ID != "bob_id" {
		t.Fatalf("Expected Bob's ID to be bob_id, got %s", bobRead.ID)
	}

	// Read All via Cursor
	readAllQuery := storage.Query{
		Action:     storage.ActionReadAll,
		Table:      "user",
		Conditions: []storage.Condition{storage.Eq("Name", "Bob")},
	}
	rows, err := db.Query("", readAllQuery, &User{}, func() Model { return &User{} })
	if err != nil {
		t.Fatalf("ReadAll by Name failed: %v", err)
	}
	defer rows.Close()

	var users []User
	for rows.Next() {
		var u User
		err := rows.Scan(&u.ID, &u.Name, &u.Email)
		if err != nil {
			t.Fatalf("Scan failed: %v", err)
		}
		users = append(users, u)
	}

	if len(users) != 1 {
		t.Fatalf("Expected 1 user, got %d", len(users))
	}
}

// Test close execution branch
func TestCloseDb(t *testing.T) {
	db := SetupDB(t, "close_db", &User{})
	_ = db.Close()
	// Should not panic
}

// Extra ReadAll edge cases
func TestReadAllEdgeCases(t *testing.T) {
	db := SetupDB(t, "readall_edge", &User{})

	user := User{ID: "edge1", Name: "Edge1"}
	query := storage.Query{
		Action:  storage.ActionCreate,
		Table:   "user",
		Columns: []string{"ID", "Name", "Email"},
		Values:  []any{user.ID, user.Name, user.Email},
	}
	_ = db.Exec("", query, &user)
}
