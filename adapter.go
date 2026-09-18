//go:build wasm

package indexdb

import (
	"sync"
	"syscall/js"

	"webtyp.com/fmt"
	. "webtyp.com/model"
	"webtyp.com/storage"
)

type adapter struct {
	dbName string
	db     js.Value
	tables []any
	logger func(...any)
	idGen  IDGenerator

	compiler *compiler

	initDone      chan struct{}
	initOnce      sync.Once
	initCompleted bool
}

// Exec implements storage.Executor
func (d *adapter) Exec(query string, args ...any) error {
	if len(args) == 0 {
		return fmt.Err("no query passed")
	}
	q, ok := args[0].(storage.Query)
	if !ok {
		return fmt.Err("invalid query type")
	}
	if len(args) < 2 {
		return fmt.Err("missing model argument")
	}
	m, ok := args[1].(Model)
	if !ok {
		return fmt.Err("invalid model type")
	}

	return d.execute(q, m, nil, nil, nil)
}

// copyPointers copies each src[i] (a typed pointer, from a Model's own
// Pointers()) into dest[i] (the caller's typed pointer, from Scan's variadic
// args). It exists because a query's read path populates its own throwaway
// Model instance internally (execute→mapResult, keyed off whatever Model was
// threaded through Compile's Plan.Args) — Scan's job is getting that data into
// the pointers the CALLER actually asked for, which are a different set.
func copyPointers(dest, src []any) error {
	if len(dest) != len(src) {
		return fmt.Err("scan destination mismatch")
	}
	for i, srcPtr := range src {
		destPtr := dest[i]
		switch d := destPtr.(type) {
		case *string:
			*d = *(srcPtr.(*string))
		case *int:
			*d = *(srcPtr.(*int))
		case *int64:
			*d = *(srcPtr.(*int64))
		case *float64:
			*d = *(srcPtr.(*float64))
		case *bool:
			*d = *(srcPtr.(*bool))
		case *[]byte:
			*d = *(srcPtr.(*[]byte))
		case *any:
			*d = *(srcPtr.(*any))
		default:
			return fmt.Err("indexdb: unsupported scan destination type", fmt.Sprintf("%T", destPtr))
		}
	}
	return nil
}

// simpleScanner implements storage.Scanner
type simpleScanner struct {
	err error
	m   Model // populated by execute() before Scan is called; nil on an error path
}

func (s *simpleScanner) Scan(dest ...any) error {
	if s.err != nil {
		return s.err
	}
	if len(dest) == 0 {
		return nil // matches the "already scanned m by reference" calling style
	}
	if s.m == nil {
		return fmt.Err("indexdb: Scan called on a result with no data")
	}
	return copyPointers(dest, s.m.Pointers())
}

// QueryRow implements storage.Executor
func (d *adapter) QueryRow(query string, args ...any) storage.Scanner {
	if len(args) == 0 {
		return &simpleScanner{err: fmt.Err("no query passed")}
	}
	q, ok := args[0].(storage.Query)
	if !ok {
		return &simpleScanner{err: fmt.Err("invalid query type")}
	}
	if len(args) < 2 {
		return &simpleScanner{err: fmt.Err("missing model argument")}
	}
	m, ok := args[1].(Model)
	if !ok {
		return &simpleScanner{err: fmt.Err("invalid model type")}
	}

	err := d.execute(q, m, nil, nil, nil)
	return &simpleScanner{err: err, m: m}
}

// simpleRows implements storage.Rows
type simpleRows struct {
	models []Model
	values []js.Value
	fields []Field
	idx    int
}

func (r *simpleRows) Next() bool {
	count := len(r.models)
	if count == 0 {
		count = len(r.values)
	}
	if r.idx < count {
		r.idx++
		return true
	}
	return false
}

func (r *simpleRows) Scan(dest ...any) error {
	if r.idx == 0 || (r.idx > len(r.models) && r.idx > len(r.values)) {
		return fmt.Err("invalid row cursor")
	}

	if len(r.models) > 0 {
		m := r.models[r.idx-1]
		return copyPointers(dest, m.Pointers())
	}

	val := r.values[r.idx-1]
	if len(r.fields) != len(dest) {
		return fmt.Err("scan destination mismatch with fields")
	}

	for i, field := range r.fields {
		jsVal := val.Get(field.Name)
		if jsVal.IsUndefined() {
			continue
		}

		destPtr := dest[i]
		switch field.Type.Storage() {
		case FieldText, FieldRaw:
			if p, ok := destPtr.(*string); ok {
				*p = jsVal.String()
			}
		case FieldInt:
			if p, ok := destPtr.(*int64); ok {
				*p = int64(jsVal.Int())
			} else if p, ok := destPtr.(*int); ok {
				*p = jsVal.Int()
			}
		case FieldFloat:
			if p, ok := destPtr.(*float64); ok {
				*p = jsVal.Float()
			}
		case FieldBool:
			if p, ok := destPtr.(*bool); ok {
				*p = jsVal.Bool()
			}
		case FieldBlob:
			if p, ok := destPtr.(*[]byte); ok {
				n := jsVal.Get("length").Int()
				b := make([]byte, n)
				if n > 0 {
					js.CopyBytesToGo(b, jsVal)
				}
				*p = b
			}
		default:
			return fmt.Err("indexdb: unsupported field type", field.Type.Storage().String(), "in column", field.Name)
		}
	}

	return nil
}

func (r *simpleRows) Columns() ([]string, error) {
	cols := make([]string, len(r.fields))
	for i, f := range r.fields {
		cols[i] = f.Name
	}
	return cols, nil
}

func (r *simpleRows) Close() error { return nil }
func (r *simpleRows) Err() error   { return nil }

// Query implements storage.Executor
func (d *adapter) Query(query string, args ...any) (storage.Rows, error) {
	if len(args) == 0 {
		return nil, fmt.Err("no query passed")
	}
	q, ok := args[0].(storage.Query)
	if !ok {
		return nil, fmt.Err("invalid query type")
	}
	if len(args) < 2 {
		return nil, fmt.Err("missing model argument")
	}
	m, ok := args[1].(Model)
	if !ok {
		return nil, fmt.Err("invalid model type")
	}

	var models []Model
	var values []js.Value
	var factory func() Model

	if len(args) > 2 {
		if f, ok := args[2].(func() Model); ok {
			factory = f
		}
	}

	var each func(Model)
	var eachJS func(js.Value)

	if factory != nil {
		each = func(model Model) {
			models = append(models, model)
		}
	} else {
		eachJS = func(val js.Value) {
			values = append(values, val)
		}
	}

	err := d.execute(q, m, factory, each, eachJS)
	if err != nil {
		return nil, err
	}

	return &simpleRows{
		models: models,
		values: values,
		fields: m.Schema(),
		idx:    0,
	}, nil
}

// Close implements storage.Executor
func (d *adapter) Close() error {
	if d.db.Truthy() {
		d.db.Call("close")
	}
	return nil
}

// newAdapter creates a new adapter.
func newAdapter(dbName string, idg IDGenerator, logger func(...any)) *adapter {
	if logger == nil {
		logger = func(args ...any) {}
	}

	return &adapter{
		dbName:   dbName,
		db:       js.Value{},
		idGen:    idg,
		logger:   logger,
		initDone: make(chan struct{}),
	}
}

// Compiler converts storage queries into engine instructions.
type compiler struct{}

func (c *compiler) Compile(q storage.Query, m Model) (storage.Plan, error) {
	// Our adapter executes queries directly. We can pass the query and model as args in the plan.
	return storage.Plan{Mode: q.Action, Query: "", Args: []any{q, m}}, nil
}

func (d *adapter) Compile(q storage.Query, m Model) (storage.Plan, error) {
	return d.compiler.Compile(q, m)
}

// New initializes the IndexedDB database and returns a storage.Conn instance.
func New(dbName string, idg IDGenerator, logger func(...any), structTables ...any) storage.Conn {
	adapter := newAdapter(dbName, idg, logger)
	adapter.compiler = &compiler{}
	adapter.initialize(structTables...)
	return adapter
}

// initialize initializes the IndexedDB database and creates object stores based on the provided structs.
func (d *adapter) initialize(structTables ...any) {
	d.tables = structTables

	// Open connection to IndexedDB
	req := js.Global().Get("indexedDB").Call("open", d.dbName)

	// Add event listeners
	req.Call("addEventListener", "error", js.FuncOf(d.onShowDbError))
	req.Call("addEventListener", "success", js.FuncOf(d.onOpenExistingDB))
	req.Call("addEventListener", "upgradeneeded", js.FuncOf(d.onUpgradeNeeded))

	// Wait until init is done
	<-d.initDone
}

func (d *adapter) open(p *js.Value, message string) error {
	d.db = p.Get("target").Get("result")

	if !d.db.Truthy() {
		return fmt.Err("error open", d.dbName, message)
	}
	return nil
}

func (d *adapter) onUpgradeNeeded(this js.Value, p []js.Value) any {
	// The event is fired on the request object, so 'this' is the request.
	// p[0] is the event object.

	// We need to set d.db before creating tables, as the connection is opened in upgrade needed transaction
	err := d.open(&p[0], "upgradeneeded")
	if err != nil {
		d.logger(err)
		return nil
	}

	for i, table := range d.tables {
		m, ok := table.(Model)
		if !ok {
			d.logger("table", i, "does not implement Model interface, skipping")
			continue
		}

		err := d.createTable(m)
		if err != nil {
			d.logger(err)
			continue
		}
	}

	// Wait for the version change transaction to complete
	transaction := p[0].Get("target").Get("transaction")
	transaction.Call("addEventListener", "complete", js.FuncOf(func(this js.Value, p []js.Value) any {
		d.initOnce.Do(func() { d.initCompleted = true; close(d.initDone) })
		return nil
	}))
	transaction.Call("addEventListener", "error", js.FuncOf(func(this js.Value, p []js.Value) any {
		d.logger("version change transaction error")
		d.initOnce.Do(func() { d.initCompleted = true; close(d.initDone) })
		return nil
	}))
	transaction.Call("addEventListener", "abort", js.FuncOf(func(this js.Value, p []js.Value) any {
		d.logger("version change transaction aborted")
		d.initOnce.Do(func() { d.initCompleted = true; close(d.initDone) })
		return nil
	}))

	return nil
}

func (d *adapter) onShowDbError(this js.Value, p []js.Value) any {
	d.logger("indexDB Error", p[0])
	return nil
}

func (d *adapter) onOpenExistingDB(this js.Value, p []js.Value) any {
	err := d.open(&p[0], "OPEN")
	if err != nil {
		d.logger("open existing db error:", err)
		return nil
	}

	if !d.initCompleted {
		d.logger("open existing db success")
	}

	d.initOnce.Do(func() { d.initCompleted = true; close(d.initDone) })
	return nil
}

// createTable creates an IndexedDB object store from the model's Schema.
func (d *adapter) createTable(m Model) error {
	if d.initCompleted {
		return fmt.Err("Dynamic table creation after initialization is not supported in IndexedDB adapter")
	}

	fields := m.Schema()
	tableName := m.ModelName()

	pkName := ""
	for _, f := range fields {
		if f.IsPK() {
			pkName = f.Name
			break
		}
	}
	if pkName == "" {
		return fmt.Err("no primary key found in schema for table", tableName)
	}

	autoIncrement := false
	for _, f := range fields {
		if f.IsAutoInc() {
			autoIncrement = true
			break
		}
	}

	opts := map[string]interface{}{"keyPath": pkName}
	if autoIncrement {
		opts["autoIncrement"] = true
	}
	newStore := d.db.Call("createObjectStore", tableName, opts)

	for _, f := range fields {
		if f.Name == pkName {
			continue
		}
		newStore.Call("createIndex", f.Name, f.Name, map[string]interface{}{"unique": f.IsUnique()})
	}
	return nil
}

// tableExist checks if a table exists in the database
func (d *adapter) tableExist(tableName string) bool {
	if !d.db.Truthy() {
		return false
	}
	// Get the list of object store names from the database
	objectStoreNames := d.db.Get("objectStoreNames")
	length := objectStoreNames.Length()

	// Iterate through the table names and check if the table already exists
	for i := 0; i < length; i++ {
		name := objectStoreNames.Index(i).String()
		if name == tableName {
			return true
		}
	}

	return false
}

// getNewID helper to access the ID generator
func (d *adapter) getNewID() string {
	if d.idGen != nil {
		return d.idGen.NewID()
	}
	return ""
}

type txBoundAdapter struct {
	*adapter
	tx       js.Value
	done     chan error
	finished bool // set once Commit or Rollback has actually run the JS call
}

func (t *txBoundAdapter) getTxStore(table, mode string) (js.Value, error) {
	return t.adapter.storeFrom(t.tx, table)
}

func (t *txBoundAdapter) Exec(query string, args ...any) error {
	if len(args) == 0 {
		return fmt.Err("no query passed")
	}
	q, ok := args[0].(storage.Query)
	if !ok {
		return fmt.Err("invalid query type")
	}
	if len(args) < 2 {
		return fmt.Err("missing model argument")
	}
	m, ok := args[1].(Model)
	if !ok {
		return fmt.Err("invalid model type")
	}

	return t.executeWithStore(t.getTxStore, q, m, nil, nil, nil)
}

func (t *txBoundAdapter) QueryRow(query string, args ...any) storage.Scanner {
	if len(args) == 0 {
		return &simpleScanner{err: fmt.Err("no query passed")}
	}
	q, ok := args[0].(storage.Query)
	if !ok {
		return &simpleScanner{err: fmt.Err("invalid query type")}
	}
	if len(args) < 2 {
		return &simpleScanner{err: fmt.Err("missing model argument")}
	}
	m, ok := args[1].(Model)
	if !ok {
		return &simpleScanner{err: fmt.Err("invalid model type")}
	}

	err := t.executeWithStore(t.getTxStore, q, m, nil, nil, nil)
	return &simpleScanner{err: err, m: m}
}

func (t *txBoundAdapter) Query(query string, args ...any) (storage.Rows, error) {
	if len(args) == 0 {
		return nil, fmt.Err("no query passed")
	}
	q, ok := args[0].(storage.Query)
	if !ok {
		return nil, fmt.Err("invalid query type")
	}
	if len(args) < 2 {
		return nil, fmt.Err("missing model argument")
	}
	m, ok := args[1].(Model)
	if !ok {
		return nil, fmt.Err("invalid model type")
	}

	var models []Model
	var values []js.Value
	var factory func() Model

	if len(args) > 2 {
		if f, ok := args[2].(func() Model); ok {
			factory = f
		}
	}

	var each func(Model)
	var eachJS func(js.Value)

	if factory != nil {
		each = func(model Model) {
			models = append(models, model)
		}
	} else {
		eachJS = func(val js.Value) {
			values = append(values, val)
		}
	}

	err := t.executeWithStore(t.getTxStore, q, m, factory, each, eachJS)
	if err != nil {
		return nil, err
	}

	return &simpleRows{
		models: models,
		values: values,
		fields: m.Schema(),
		idx:    0,
	}, nil
}

// BeginTx opens ONE IndexedDB transaction spanning every declared object store
// and returns an executor bound to it. IndexedDB auto-commits a transaction as
// soon as the event loop yields with no pending request, so the bound executor
// must issue its requests back-to-back and must not await anything unrelated.
func (d *adapter) BeginTx() (storage.TxBoundExecutor, error) {
	if !d.db.Truthy() {
		return nil, fmt.Err("Database not initialized")
	}

	storeNames := d.db.Get("objectStoreNames")
	if !storeNames.Truthy() || storeNames.Get("length").Int() == 0 {
		return nil, fmt.Err("No object stores found")
	}

	tx := d.db.Call("transaction", storeNames, "readwrite")
	if !tx.Truthy() {
		return nil, fmt.Err("Failed to create transaction")
	}

	txBound := &txBoundAdapter{
		adapter: d,
		tx:      tx,
		done:    make(chan error, 1),
	}

	onComplete := js.FuncOf(func(this js.Value, args []js.Value) any {
		select {
		case txBound.done <- nil:
		default:
		}
		return nil
	})
	onError := js.FuncOf(func(this js.Value, args []js.Value) any {
		errVal := tx.Get("error")
		errMsg := "transaction error"
		if errVal.Truthy() {
			errMsg = errVal.Get("message").String()
		}
		select {
		case txBound.done <- fmt.Err("indexdb tx failed:", errMsg):
		default:
		}
		return nil
	})
	onAbort := js.FuncOf(func(this js.Value, args []js.Value) any {
		select {
		case txBound.done <- fmt.Err("indexdb tx aborted"):
		default:
		}
		return nil
	})

	tx.Call("addEventListener", "complete", onComplete)
	tx.Call("addEventListener", "error", onError)
	tx.Call("addEventListener", "abort", onAbort)

	return txBound, nil
}

// Commit is a no-op if the transaction already finished (storage.TxBoundExecutor).
func (t *txBoundAdapter) Commit() error {
	if t.finished {
		return nil
	}
	t.finished = true
	if commitFn := t.tx.Get("commit"); commitFn.Truthy() && !commitFn.IsUndefined() {
		t.tx.Call("commit")
	}
	return <-t.done
}

// Rollback is a no-op if the transaction already finished (storage.TxBoundExecutor).
// Without this check, `defer tx.Rollback()` right after a successful Commit — the
// standard Go idiom, matching database/sql — calls tx.abort() on an IndexedDB
// transaction that has already completed, which throws
// "Failed to execute 'abort' on 'IDBTransaction': The transaction has finished."
func (t *txBoundAdapter) Rollback() error {
	if t.finished {
		return nil
	}
	t.finished = true
	t.tx.Call("abort")
	<-t.done
	return nil
}

var _ storage.Conn = (*adapter)(nil)
var _ storage.TxExecutor = (*adapter)(nil)
var _ storage.TxBoundExecutor = (*txBoundAdapter)(nil)
