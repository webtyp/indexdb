//go:build wasm

package indexdb

import (
	"syscall/js"

	"webtyp.com/fmt"
)

// processCursorRequest handles an IndexedDB cursor request (openCursor).
// It iterates over the cursor and calls the provided callback for each item.
func processCursorRequest(req js.Value, onNext func(cursor js.Value) bool) error {
	done := make(chan struct{})
	var err error

	// We need a persistent callback for 'success' because it's called multiple times for a cursor
	// However, since we are using 'continue' which triggers another success event on the SAME request object,
	// we can just reuse the same callback.

	var onSuccess js.Func
	onSuccess = js.FuncOf(func(this js.Value, args []js.Value) any {
		cursor := req.Get("result")

		if !cursor.Truthy() {
			// End of cursor iteration
			close(done)
			return nil
		}

		// Process current item
		shouldContinue := onNext(cursor)

		if shouldContinue {
			cursor.Call("continue")
		} else {
			// Stop iteration explicitly
			close(done)
		}
		return nil
	})
	defer onSuccess.Release()

	onError := js.FuncOf(func(this js.Value, args []js.Value) any {
		errVal := req.Get("error")
		errMsg := "Unknown IndexedDB cursor error"
		if errVal.Truthy() {
			errMsg = errVal.Get("message").String()
		}
		err = fmt.Err("IndexedDB cursor failed:", errMsg)
		// Only close done on error if not already closed
		select {
		case <-done:
		default:
			close(done)
		}
		return nil
	})
	defer onError.Release()

	req.Call("addEventListener", "success", onSuccess)
	req.Call("addEventListener", "error", onError)

	<-done
	return err
}

// storeFrom resolves an object store from an active transaction.
func (d *adapter) storeFrom(tx js.Value, table string) (js.Value, error) {
	if !tx.Truthy() {
		return js.Value{}, fmt.Err("Transaction not valid")
	}
	store := tx.Call("objectStore", table)
	if !store.Truthy() {
		return js.Value{}, fmt.Err("Failed to get object store for table", table)
	}
	return store, nil
}

// Transaction helper to start a transaction and get the object store.
// mode should be "readonly" or "readwrite".
func (d *adapter) getStore(tableName string, mode string) (js.Value, error) {
	if !d.db.Truthy() {
		return js.Value{}, fmt.Err("Database not initialized")
	}

	// Pre-check object store existence. Calling transaction() with an unknown
	// store throws a NotFoundError, which is unrecoverable under TinyGo wasm
	// (recover() not supported on this target — see tinygo.org/docs/reference/lang-support).
	storeNames := d.db.Get("objectStoreNames")
	if !storeNames.Truthy() || !storeNames.Call("contains", tableName).Bool() {
		return js.Value{}, fmt.Err("Object store", tableName, "not found")
	}

	tx := d.db.Call("transaction", tableName, mode)
	if !tx.Truthy() {
		return js.Value{}, fmt.Err("Failed to create transaction for table", tableName)
	}

	return d.storeFrom(tx, tableName)
}
