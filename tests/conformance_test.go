//go:build wasm

package tests_test

import (
	"fmt"
	"testing"

	"webtyp.com/model"
	"webtyp.com/storage"
	dbconf "webtyp.com/storage/conformance"
)

func TestIndexDB_DBConformance(t *testing.T) {
	var n int
	dbconf.Run(t, dbconf.Factory{
		Name: "indexdb",
		New: func(t *testing.T, models ...model.Model) storage.Conn {
			n++
			dbName := fmt.Sprintf("conformance_db_%d", n) // fresh IndexedDB per clause
			return SetupDB(t, dbName, models...) // from tests/setup_test.go
		},
	})
}
