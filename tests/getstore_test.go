//go:build wasm

package tests_test

import (
	"strings"
	"testing"

	"webtyp.com/storage"
)

func TestGetStore_ExistingTable_Succeeds(t *testing.T) {
	db := SetupDB(t, "test_getstore_existing", &User{})
	defer db.Close()

	query := storage.Query{
		Action:  storage.ActionCreate,
		Table:   "user",
		Columns: []string{"ID", "Name", "Email"},
		Values:  []any{"1", "Alice", "alice@example.com"},
	}
	if err := db.Exec("", query, &User{}); err != nil {
		t.Fatalf("Expected no error obtaining store for existing table, got: %v", err)
	}
}

func TestGetStore_MissingTable_ReturnsError(t *testing.T) {
	db := SetupDB(t, "test_getstore_missing", &User{})
	defer db.Close()

	query := storage.Query{
		Action:     storage.ActionReadOne,
		Table:      "nonexistent",
		Conditions: []storage.Condition{storage.Eq("ID", "1")},
	}
	err := db.QueryRow("", query, &User{}).Scan()
	if err == nil {
		t.Fatal("Expected error for missing table, got nil")
	}

	// Case-insensitive check because webtyp/fmt might translate words
	errMsg := strings.ToLower(err.Error())
	if !strings.Contains(errMsg, "nonexistent") || !strings.Contains(errMsg, "not found") {
		t.Errorf("Expected error message to contain 'nonexistent' and 'not found', got: %v", err.Error())
	}
}
