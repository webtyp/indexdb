//go:build wasm

package tests_test

import (
	"testing"

	"webtyp.com/model"
	"webtyp.com/storage"
)

func execQuery(t *testing.T, db storage.Conn, q storage.Query) {
	t.Helper()
	if _, err := db.Exec("", q, &User{}); err != nil {
		t.Fatalf("%v: %v", q.Action, err)
	}
}

func storedTextSearch(t *testing.T, db storage.Conn, id string) string {
	t.Helper()
	rows, err := db.Query("", storage.Query{Action: storage.ActionReadAll, Table: "user", Columns: []string{model.FieldTextSearch}, Conditions: []storage.Condition{storage.Eq("ID", id)}}, &User{})
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatal("no row")
	}
	var got string
	if err := rows.Scan(&got); err != nil {
		t.Fatal(err)
	}
	return got
}

func TestTextSearch_UpdateByKeyKeepsIt(t *testing.T) {
	db := SetupDB(t, "test_text_search_update_by_key", &User{})
	defer db.Close()
	execQuery(t, db, storage.Query{Action: storage.ActionCreate, Table: "user", Columns: []string{"ID", "Name", model.FieldTextSearch}, Values: []any{"1", "Ana", "ana"}})
	execQuery(t, db, storage.Query{Action: storage.ActionUpdate, Table: "user", Columns: []string{"Email"}, Values: []any{"a@b.cl"}, Conditions: []storage.Condition{storage.Eq("ID", "1")}})
	if got := storedTextSearch(t, db, "1"); got != "ana" {
		t.Errorf("text_search = %q, want ana", got)
	}
}

func TestTextSearch_UpdateByCursorKeepsIt(t *testing.T) {
	db := SetupDB(t, "test_text_search_update_by_cursor", &User{})
	defer db.Close()
	execQuery(t, db, storage.Query{Action: storage.ActionCreate, Table: "user", Columns: []string{"ID", "Name", model.FieldTextSearch}, Values: []any{"1", "Ana", "ana"}})
	execQuery(t, db, storage.Query{Action: storage.ActionUpdate, Table: "user", Columns: []string{"Email"}, Values: []any{"a@b.cl"}, Conditions: []storage.Condition{storage.Eq("Name", "Ana")}})
	if got := storedTextSearch(t, db, "1"); got != "ana" {
		t.Errorf("text_search = %q, want ana", got)
	}
}

func TestTextSearch_LikeFindsIt(t *testing.T) {
	db := SetupDB(t, "test_text_search_like", &User{})
	defer db.Close()
	execQuery(t, db, storage.Query{Action: storage.ActionCreate, Table: "user", Columns: []string{"ID", "Name", model.FieldTextSearch}, Values: []any{"1", "Lucía", "lucia"}})
	execQuery(t, db, storage.Query{Action: storage.ActionCreate, Table: "user", Columns: []string{"ID", "Name", model.FieldTextSearch}, Values: []any{"2", "Bruno", "bruno"}})
	rows, err := db.Query("", storage.Query{Action: storage.ActionReadAll, Table: "user", Columns: []string{"ID"}, Conditions: []storage.Condition{storage.Like(model.FieldTextSearch, "%luc%")}}, &User{})
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if len(ids) != 1 || ids[0] != "1" {
		t.Errorf("ids = %v, want [1]", ids)
	}
}
