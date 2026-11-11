//go:build wasm

package tests_test

import (
	"testing"

	"webtyp.com/model"
	"webtyp.com/storage"
)

func TestILike_IgnoresCase(t *testing.T) {
	db := SetupDB(t, "test_ilike_ignores_case", &User{})
	defer db.Close()

	users := []User{
		{ID: "1", Name: "Ana"},
		{ID: "2", Name: "ana maría"},
		{ID: "3", Name: "Bob"},
	}

	for _, u := range users {
		query := storage.Query{
			Action:  storage.ActionCreate,
			Table:   "user",
			Columns: []string{"ID", "Name"},
			Values:  []any{u.ID, u.Name},
		}
		if _, err := db.Exec("", query, &u); err != nil {
			t.Fatalf("Failed to insert user %v: %v", u.Name, err)
		}
	}

	readAllQuery := storage.Query{
		Action:     storage.ActionReadAll,
		Table:      "user",
		Conditions: []storage.Condition{storage.ILike("Name", "%AN%")},
	}
	rows, err := db.Query("", readAllQuery, &User{}, func() model.Model { return &User{} })
	if err != nil {
		t.Fatalf("Query failed: %v", err)
	}
	defer rows.Close()

	var result []User
	for rows.Next() {
		var u User
		err := rows.Scan(&u.ID, &u.Name, &u.Email) // Email is scanned but unused in values
		if err != nil {
			t.Fatalf("Scan failed: %v", err)
		}
		result = append(result, u)
	}

	if len(result) != 2 {
		t.Fatalf("Expected 2 users, got %d", len(result))
	}

	foundAna := false
	foundAnaMaria := false
	for _, u := range result {
		if u.Name == "Ana" {
			foundAna = true
		} else if u.Name == "ana maría" {
			foundAnaMaria = true
		}
	}

	if !foundAna || !foundAnaMaria {
		t.Fatalf("Expected results to contain 'Ana' and 'ana maría', got %v", result)
	}
}
