//go:build wasm

package tests_test

import (
	"testing"
	"webtyp.com/indexdb"
	"webtyp.com/model"
	"webtyp.com/storage"
)

type UpgradeModelA struct {
	ID   string
	Name string
}

func (u *UpgradeModelA) ModelName() string { return "a" }
func (u *UpgradeModelA) Schema() []model.Field {
	return []model.Field{
		{Name: "ID", Type: model.Text(), DB: &model.FieldDB{PK: true}},
		{Name: "Name", Type: model.Text()},
	}
}
func (u *UpgradeModelA) Values() []any               { return []any{u.ID, u.Name} }
func (u *UpgradeModelA) Pointers() []any             { return []any{&u.ID, &u.Name} }
func (u *UpgradeModelA) EncodeFields(wr model.FieldWriter) {}
func (u *UpgradeModelA) DecodeFields(r model.FieldReader)  {}
func (u *UpgradeModelA) IsNil() bool                 { return u == nil }

type UpgradeModelB struct {
	ID   string
	Desc string
}

func (b *UpgradeModelB) ModelName() string { return "b" }
func (b *UpgradeModelB) Schema() []model.Field {
	return []model.Field{
		{Name: "ID", Type: model.Text(), DB: &model.FieldDB{PK: true}},
		{Name: "Desc", Type: model.Text()},
	}
}
func (b *UpgradeModelB) Values() []any               { return []any{b.ID, b.Desc} }
func (b *UpgradeModelB) Pointers() []any             { return []any{&b.ID, &b.Desc} }
func (b *UpgradeModelB) EncodeFields(wr model.FieldWriter) {}
func (b *UpgradeModelB) DecodeFields(r model.FieldReader)  {}
func (b *UpgradeModelB) IsNil() bool                 { return b == nil }

func TestUpgrade(t *testing.T) {
	dbName := "upgrade_test_db"

	// 1. New(name, &A{}), create a row, Close()
	db1, err := indexdb.New(dbName, &UpgradeModelA{})
	if err != nil {
		t.Fatal(err)
	}

	// Insert row in A
	err = db1.Exec("", storage.Query{Action: storage.ActionCreate, Table: "a", Columns: []string{"ID", "Name"}, Values: []any{"1", "A1"}}, &UpgradeModelA{ID: "1", Name: "A1"})
	if err != nil {
		t.Fatal(err)
	}
	db1.Close()

	// 2. New(name, &A{}, &B{}) -> no error, read A, write B, read B
	db2, err := indexdb.New(dbName, &UpgradeModelA{}, &UpgradeModelB{})
	if err != nil {
		t.Fatal(err)
	}

	var readA UpgradeModelA
	err = db2.QueryRow("", storage.Query{Action: storage.ActionReadOne, Table: "a", Conditions: []storage.Condition{storage.Eq("ID", "1")}}, &UpgradeModelA{}).Scan(&readA.ID, &readA.Name)
	if err != nil {
		t.Fatal(err)
	}
	if readA.Name != "A1" {
		t.Fatalf("expected A1, got %v", readA.Name)
	}

	err = db2.Exec("", storage.Query{Action: storage.ActionCreate, Table: "b", Columns: []string{"ID", "Desc"}, Values: []any{"1", "B1"}}, &UpgradeModelB{ID: "1", Desc: "B1"})
	if err != nil {
		t.Fatal(err)
	}

	var readB UpgradeModelB
	err = db2.QueryRow("", storage.Query{Action: storage.ActionReadOne, Table: "b", Conditions: []storage.Condition{storage.Eq("ID", "1")}}, &UpgradeModelB{}).Scan(&readB.ID, &readB.Desc)
	if err != nil {
		t.Fatal(err)
	}
	if readB.Desc != "B1" {
		t.Fatalf("expected B1, got %v", readB.Desc)
	}
	db2.Close()

	// 3. New(name, &A{}) again (B no longer declared) -> no error, A exists, B exists
	db3, err := indexdb.New(dbName, &UpgradeModelA{})
	if err != nil {
		t.Fatal(err)
	}
	db3.Close()

	// Read B through full New to prove it exists
	db4, err := indexdb.New(dbName, &UpgradeModelA{}, &UpgradeModelB{})
	if err != nil {
		t.Fatal(err)
	}
	var readB4 UpgradeModelB
	err = db4.QueryRow("", storage.Query{Action: storage.ActionReadOne, Table: "b", Conditions: []storage.Condition{storage.Eq("ID", "1")}}, &UpgradeModelB{}).Scan(&readB4.ID, &readB4.Desc)
	if err != nil {
		t.Fatal(err)
	}
	if readB4.Desc != "B1" {
		t.Fatalf("expected B1, got %v", readB4.Desc)
	}
	db4.Close()
}

func TestNewErrors(t *testing.T) {
	// zero models
	_, err := indexdb.New("test")
	if err == nil || err.Error() != "indexdb: New requires at least one model" {
		t.Fatalf("expected zero models error, got: %v", err)
	}

	// duplicate models
	_, err = indexdb.New("test", &UpgradeModelA{}, &UpgradeModelA{})
	if err == nil || err.Error() != "indexdb: duplicate model name a" {
		t.Fatalf("expected duplicate model error, got: %v", err)
	}
}
