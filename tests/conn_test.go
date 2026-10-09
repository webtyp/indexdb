//go:build wasm

package tests_test

import (
	"testing"

	"webtyp.com/storage"
)

func TestExecuteActionNotImplemented(t *testing.T) {
	db := SetupDB(t, "action_not_implemented_test", &User{})

	err := db.Exec("", storage.Query{Action: 999}, &User{})
	if err == nil {
		t.Fatal("Expected error for unimplemented action")
	}
}

func TestActionsOnMissingTable(t *testing.T) {
	db := SetupDB(t, "missing_table_actions_test", &User{})

	cases := []struct {
		name  string
		query storage.Query
	}{
		{"Create", storage.Query{Action: storage.ActionCreate, Table: "non_existent"}},
		{"Update", storage.Query{Action: storage.ActionUpdate, Table: "non_existent"}},
		{"Delete", storage.Query{Action: storage.ActionDelete, Table: "non_existent"}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := db.Exec("", c.query, &User{}); err == nil {
				t.Fatalf("Expected error for %s on non-existent table", c.name)
			}
		})
	}

	t.Run("ReadOne", func(t *testing.T) {
		q := storage.Query{Action: storage.ActionReadOne, Table: "non_existent"}
		if err := db.QueryRow("", q, &User{}).Scan(); err == nil {
			t.Fatal("Expected error for ReadOne on non-existent table")
		}
	})

	t.Run("ReadAll", func(t *testing.T) {
		q := storage.Query{Action: storage.ActionReadAll, Table: "non_existent"}
		if _, err := db.Query("", q, &User{}); err == nil {
			t.Fatal("Expected error for ReadAll on non-existent table")
		}
	})
}

func TestConnMisuseErrors(t *testing.T) {
	db := SetupDB(t, "conn_misuse_test", &User{})

	if _, err := db.Query("", storage.Query{}); err == nil {
		t.Fatal("Expected error on invalid args to Query (missing model)")
	}

	if err := db.Exec(""); err == nil {
		t.Fatal("Expected error on Exec without query")
	}
}
