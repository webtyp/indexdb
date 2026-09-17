//go:build wasm

package tests_test

import (
	"bytes"
	"fmt"
	"testing"

	. "webtyp.com/model"
	"webtyp.com/storage"
)

type VectorItem struct {
	ID   string
	Name string
	Vec  []byte
}

func (v *VectorItem) ModelName() string { return "vector_item" }
func (v *VectorItem) Schema() []Field {
	return []Field{
		{Name: "ID", Type: Text(), DB: &FieldDB{PK: true}},
		{Name: "Name", Type: Text()},
		{Name: "Vec", Type: Blob()},
	}
}
func (v *VectorItem) Values() []any               { return []any{v.ID, v.Name, v.Vec} }
func (v *VectorItem) Pointers() []any             { return []any{&v.ID, &v.Name, &v.Vec} }
func (v *VectorItem) EncodeFields(wr FieldWriter) {}
func (v *VectorItem) DecodeFields(r FieldReader)  {}
func (v *VectorItem) IsNil() bool                 { return v == nil }

type UnsupportedItem struct {
	ID   string
	Nums []int
}

func (u *UnsupportedItem) ModelName() string { return "unsupported_item" }
func (u *UnsupportedItem) Schema() []Field {
	return []Field{
		{Name: "ID", Type: Text(), DB: &FieldDB{PK: true}},
		{Name: "Nums", Type: IntSlice()},
	}
}
func (u *UnsupportedItem) Values() []any               { return []any{u.ID, u.Nums} }
func (u *UnsupportedItem) Pointers() []any             { return []any{&u.ID, &u.Nums} }
func (u *UnsupportedItem) EncodeFields(wr FieldWriter) {}
func (u *UnsupportedItem) DecodeFields(r FieldReader)  {}
func (u *UnsupportedItem) IsNil() bool                 { return u == nil }

func TestBlob_RoundTripExact(t *testing.T) {
	db := SetupDB(nil, "blob_roundtrip_test", &VectorItem{})

	buf := make([]byte, 1536)
	for i := range buf {
		buf[i] = byte(i % 256)
	}
	buf[500] = 0x00
	buf[1535] = 0xFF

	item := VectorItem{ID: "1", Name: "exact", Vec: buf}
	q := storage.Query{
		Action:  storage.ActionCreate,
		Table:   "vector_item",
		Columns: []string{"ID", "Name", "Vec"},
		Values:  []any{item.ID, item.Name, item.Vec},
	}
	if err := db.Exec("", q, &item); err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	var read VectorItem
	readQ := storage.Query{
		Action:     storage.ActionReadOne,
		Table:      "vector_item",
		Conditions: []storage.Condition{storage.Eq("ID", "1")},
	}
	if err := db.QueryRow("", readQ, &read).Scan(); err != nil {
		t.Fatalf("ReadOne failed: %v", err)
	}

	if len(read.Vec) != len(buf) {
		t.Fatalf("Expected blob length %d, got %d", len(buf), len(read.Vec))
	}
	if !bytes.Equal(read.Vec, buf) {
		t.Fatal("Retrieved blob does not match exact bytes")
	}
	if read.Vec[500] != 0x00 || read.Vec[1535] != 0xFF {
		t.Fatal("Specific byte checkpoints failed")
	}
}

func TestBlob_EmptyAndNil(t *testing.T) {
	db := SetupDB(nil, "blob_empty_nil_test", &VectorItem{})

	item1 := VectorItem{ID: "empty", Name: "empty", Vec: []byte{}}
	q1 := storage.Query{
		Action:  storage.ActionCreate,
		Table:   "vector_item",
		Columns: []string{"ID", "Name", "Vec"},
		Values:  []any{item1.ID, item1.Name, item1.Vec},
	}
	if err := db.Exec("", q1, &item1); err != nil {
		t.Fatalf("Create empty failed: %v", err)
	}

	item2 := VectorItem{ID: "nil", Name: "nil", Vec: nil}
	q2 := storage.Query{
		Action:  storage.ActionCreate,
		Table:   "vector_item",
		Columns: []string{"ID", "Name", "Vec"},
		Values:  []any{item2.ID, item2.Name, item2.Vec},
	}
	if err := db.Exec("", q2, &item2); err != nil {
		t.Fatalf("Create nil failed: %v", err)
	}

	var read1 VectorItem
	readQ1 := storage.Query{
		Action:     storage.ActionReadOne,
		Table:      "vector_item",
		Conditions: []storage.Condition{storage.Eq("ID", "empty")},
	}
	if err := db.QueryRow("", readQ1, &read1).Scan(); err != nil {
		t.Fatalf("Read empty failed: %v", err)
	}
	if len(read1.Vec) != 0 {
		t.Errorf("Expected empty slice, got length %d", len(read1.Vec))
	}

	var read2 VectorItem
	readQ2 := storage.Query{
		Action:     storage.ActionReadOne,
		Table:      "vector_item",
		Conditions: []storage.Condition{storage.Eq("ID", "nil")},
	}
	if err := db.QueryRow("", readQ2, &read2).Scan(); err != nil {
		t.Fatalf("Read nil failed: %v", err)
	}
	if len(read2.Vec) != 0 {
		t.Errorf("Expected nil/empty slice, got length %d", len(read2.Vec))
	}
}

func TestBlob_UpdateOtherColumnPreservesBlob(t *testing.T) {
	db := SetupDB(nil, "blob_update_preserve_test", &VectorItem{})

	buf := make([]byte, 1536)
	for i := range buf {
		buf[i] = byte(i)
	}

	item := VectorItem{ID: "1", Name: "initial", Vec: buf}
	createQ := storage.Query{
		Action:  storage.ActionCreate,
		Table:   "vector_item",
		Columns: []string{"ID", "Name", "Vec"},
		Values:  []any{item.ID, item.Name, item.Vec},
	}
	if err := db.Exec("", createQ, &item); err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	updateQ := storage.Query{
		Action:     storage.ActionUpdate,
		Table:      "vector_item",
		Columns:    []string{"Name"},
		Values:     []any{"updated"},
		Conditions: []storage.Condition{storage.Eq("ID", "1")},
	}
	if err := db.Exec("", updateQ, &item); err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	var read VectorItem
	readQ := storage.Query{
		Action:     storage.ActionReadOne,
		Table:      "vector_item",
		Conditions: []storage.Condition{storage.Eq("ID", "1")},
	}
	if err := db.QueryRow("", readQ, &read).Scan(); err != nil {
		t.Fatalf("ReadOne failed: %v", err)
	}

	if read.Name != "updated" {
		t.Errorf("Expected Name updated, got %s", read.Name)
	}
	if !bytes.Equal(read.Vec, buf) {
		t.Fatal("Update of text column corrupted or erased blob column!")
	}
}

func TestBlob_UpdateReplacesBytes(t *testing.T) {
	db := SetupDB(nil, "blob_update_replace_test", &VectorItem{})

	buf1 := make([]byte, 1536)
	for i := range buf1 {
		buf1[i] = 0xAA
	}

	item := VectorItem{ID: "1", Name: "item1", Vec: buf1}
	createQ := storage.Query{
		Action:  storage.ActionCreate,
		Table:   "vector_item",
		Columns: []string{"ID", "Name", "Vec"},
		Values:  []any{item.ID, item.Name, item.Vec},
	}
	if err := db.Exec("", createQ, &item); err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	buf2 := make([]byte, 1536)
	for i := range buf2 {
		buf2[i] = 0xBB
	}

	updateQ := storage.Query{
		Action:     storage.ActionUpdate,
		Table:      "vector_item",
		Columns:    []string{"Vec"},
		Values:     []any{buf2},
		Conditions: []storage.Condition{storage.Eq("ID", "1")},
	}
	if err := db.Exec("", updateQ, &item); err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	var read VectorItem
	readQ := storage.Query{
		Action:     storage.ActionReadOne,
		Table:      "vector_item",
		Conditions: []storage.Condition{storage.Eq("ID", "1")},
	}
	if err := db.QueryRow("", readQ, &read).Scan(); err != nil {
		t.Fatalf("ReadOne failed: %v", err)
	}

	if !bytes.Equal(read.Vec, buf2) {
		t.Fatal("Blob update failed to replace bytes")
	}
}

func TestBlob_EqCondition(t *testing.T) {
	db := SetupDB(nil, "blob_eq_cond_test", &VectorItem{})

	buf1 := []byte{0x01, 0x02, 0x03}
	buf2 := []byte{0x04, 0x05, 0x06}

	item1 := VectorItem{ID: "1", Name: "one", Vec: buf1}
	q1 := storage.Query{
		Action:  storage.ActionCreate,
		Table:   "vector_item",
		Columns: []string{"ID", "Name", "Vec"},
		Values:  []any{item1.ID, item1.Name, item1.Vec},
	}
	if err := db.Exec("", q1, &item1); err != nil {
		t.Fatalf("Create item1 failed: %v", err)
	}

	item2 := VectorItem{ID: "2", Name: "two", Vec: buf2}
	q2 := storage.Query{
		Action:  storage.ActionCreate,
		Table:   "vector_item",
		Columns: []string{"ID", "Name", "Vec"},
		Values:  []any{item2.ID, item2.Name, item2.Vec},
	}
	if err := db.Exec("", q2, &item2); err != nil {
		t.Fatalf("Create item2 failed: %v", err)
	}

	var read VectorItem
	readQ := storage.Query{
		Action:     storage.ActionReadOne,
		Table:      "vector_item",
		Conditions: []storage.Condition{storage.Eq("Vec", buf1)},
	}
	if err := db.QueryRow("", readQ, &read).Scan(); err != nil {
		t.Fatalf("ReadOne with Eq(Vec) failed: %v", err)
	}

	if read.ID != "1" {
		t.Errorf("Expected ID 1, got %s", read.ID)
	}
}

func TestBlob_OrderByBlobIsError(t *testing.T) {
	db := SetupDB(nil, "blob_orderby_err_test", &VectorItem{})

	item := VectorItem{ID: "1", Name: "one", Vec: []byte{0x01}}
	createQ := storage.Query{
		Action:  storage.ActionCreate,
		Table:   "vector_item",
		Columns: []string{"ID", "Name", "Vec"},
		Values:  []any{item.ID, item.Name, item.Vec},
	}
	_ = db.Exec("", createQ, &item)

	readAllQ := storage.Query{
		Action:  storage.ActionReadAll,
		Table:   "vector_item",
		OrderBy: []storage.Order{storage.Asc("Vec")},
	}
	_, err := db.Query("", readAllQ, &VectorItem{})
	if err == nil {
		t.Fatal("Expected error when ordering by blob column, got nil")
	}
}

func TestUnsupportedType_IsErrorNotPanic(t *testing.T) {
	db := SetupDB(nil, "unsupported_type_test", &UnsupportedItem{})

	item := UnsupportedItem{ID: "1", Nums: []int{1, 2, 3}}
	createQ := storage.Query{
		Action:  storage.ActionCreate,
		Table:   "unsupported_item",
		Columns: []string{"ID", "Nums"},
		Values:  []any{item.ID, item.Nums},
	}
	err := db.Exec("", createQ, &item)
	if err == nil {
		t.Fatal("Expected error for unsupported field type, got nil")
	}
}

func TestTx_BatchInsertOneTransaction(t *testing.T) {
	db := SetupDB(nil, "tx_batch_insert_test", &VectorItem{})

	txExec, ok := db.(storage.TxExecutor)
	if !ok {
		t.Fatal("db does not implement storage.TxExecutor")
	}

	tx, err := txExec.BeginTx()
	if err != nil {
		t.Fatalf("BeginTx failed: %v", err)
	}

	for i := 0; i < 1024; i++ {
		id := fmt.Sprintf("%d", i)
		v := VectorItem{ID: id, Name: "batch", Vec: []byte{byte(i % 256)}}
		q := storage.Query{
			Action:  storage.ActionCreate,
			Table:   "vector_item",
			Columns: []string{"ID", "Name", "Vec"},
			Values:  []any{v.ID, v.Name, v.Vec},
		}
		if err := tx.Exec("", q, &v); err != nil {
			t.Fatalf("Batch insert item %d failed: %v", i, err)
		}
	}

	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit failed: %v", err)
	}

	rows, err := db.Query("", storage.Query{Action: storage.ActionReadAll, Table: "vector_item"}, &VectorItem{}, func() Model { return &VectorItem{} })
	if err != nil {
		t.Fatalf("ReadAll failed: %v", err)
	}
	defer rows.Close()

	count := 0
	for rows.Next() {
		count++
	}
	if count != 1024 {
		t.Fatalf("Expected 1024 inserted rows, got %d", count)
	}
}

func TestTx_RollbackDiscards(t *testing.T) {
	db := SetupDB(nil, "tx_rollback_test", &VectorItem{})

	txExec, ok := db.(storage.TxExecutor)
	if !ok {
		t.Fatal("db does not implement storage.TxExecutor")
	}

	tx, err := txExec.BeginTx()
	if err != nil {
		t.Fatalf("BeginTx failed: %v", err)
	}

	for i := 0; i < 1024; i++ {
		id := fmt.Sprintf("%d", i)
		v := VectorItem{ID: id, Name: "batch", Vec: []byte{byte(i % 256)}}
		q := storage.Query{
			Action:  storage.ActionCreate,
			Table:   "vector_item",
			Columns: []string{"ID", "Name", "Vec"},
			Values:  []any{v.ID, v.Name, v.Vec},
		}
		if err := tx.Exec("", q, &v); err != nil {
			t.Fatalf("Insert item %d failed: %v", i, err)
		}
	}

	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback failed: %v", err)
	}

	rows, err := db.Query("", storage.Query{Action: storage.ActionReadAll, Table: "vector_item"}, &VectorItem{}, func() Model { return &VectorItem{} })
	if err != nil {
		t.Fatalf("ReadAll failed: %v", err)
	}
	defer rows.Close()

	count := 0
	for rows.Next() {
		count++
	}
	if count != 0 {
		t.Fatalf("Expected 0 rows after rollback, got %d", count)
	}
}

func TestTx_LargeShardBlob(t *testing.T) {
	db := SetupDB(nil, "tx_large_shard_test", &VectorItem{})

	largeBuf := make([]byte, 4*1024*1024)
	for i := range largeBuf {
		largeBuf[i] = byte(i % 251)
	}

	item := VectorItem{ID: "shard1", Name: "large_shard", Vec: largeBuf}
	q := storage.Query{
		Action:  storage.ActionCreate,
		Table:   "vector_item",
		Columns: []string{"ID", "Name", "Vec"},
		Values:  []any{item.ID, item.Name, item.Vec},
	}
	if err := db.Exec("", q, &item); err != nil {
		t.Fatalf("Create 4MB shard failed: %v", err)
	}

	var read VectorItem
	readQ := storage.Query{
		Action:     storage.ActionReadOne,
		Table:      "vector_item",
		Conditions: []storage.Condition{storage.Eq("ID", "shard1")},
	}
	if err := db.QueryRow("", readQ, &read).Scan(); err != nil {
		t.Fatalf("ReadOne 4MB shard failed: %v", err)
	}

	if len(read.Vec) != len(largeBuf) {
		t.Fatalf("Expected 4MB (%d bytes), got %d bytes", len(largeBuf), len(read.Vec))
	}
	if !bytes.Equal(read.Vec, largeBuf) {
		t.Fatal("4MB shard content does not match original bytes")
	}
}
