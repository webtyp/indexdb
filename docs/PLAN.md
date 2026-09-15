---
PLAN: "feat: binary (blob) columns and batched transactions"
TAG: v0.2.0
EXECUTOR: unassigned
REVIEWER: none
---

> Part of the browser-native semantic search effort. Master index:
> https://github.com/webtyp/agent/blob/main/docs/PLAN.md — decisions **D1** and **D2**
> there are the rationale for the representation chosen below.

# Plan — make IndexedDB able to carry a vector

## Why

This driver cannot store binary data, and the failure mode is the worst available.

`create` (`execute.go:44-49`) builds a `map[string]any` from `q.Columns`/`q.Values` and
hands it to `store.Call("add", data)`. `Call` converts arguments through `js.ValueOf`,
which supports `[]any` but **not `[]byte`** and **not `[]float32`** — it panics on
anything else. `getStore` (`tx.go`) already documents the consequence in this very
repository:

> Calling `transaction()` with an unknown store throws a NotFoundError, which is
> unrecoverable under TinyGo wasm (`recover()` not supported on this target).

The same applies here: a `js.ValueOf` panic on a blob column is an **unrecoverable crash
of the whole application**, not an error return. So today a `model.Blob()` column does
not merely fail to persist — it takes the page down.

Four more paths have the same shape. Each dispatches on `f.Type.Storage()` over a
`switch` that covers `FieldText`/`FieldInt`/`FieldFloat`/`FieldBool` and silently drops
everything else:

| Location | Effect on a blob column |
|---|---|
| `execute.go:update` (PK fast path, ~line 88) | the field is dropped from `data`, so an update to any other column **erases the vector** |
| `execute.go:update` (cursor path, ~line 130) | same erasure |
| `adapter.go:simpleRows.Scan` (~line 147) | the blob is skipped; the destination keeps its previous value |
| `execute.go:checkCondition` | `val.Type()` is `js.TypeObject` for a `Uint8Array` → `return false`, so a blob never matches any condition, including `IS NOT NULL` |

`mapResult` delegates to `jsvalue.ScanValue`, whose `Uint8Array` support is **unverified
and is the first task below**.

Separately: `getStore` opens a **new transaction per call**, so inserting a shard set of
1024 rows costs 1024 transactions. IndexedDB transactions auto-commit when the event loop
yields, so this is not just slow, it is non-atomic: a failure halfway leaves the store in
a torn state with no way to roll back.

## What does NOT change

`processCursorRequest` (`tx.go`) stays exactly as it is. The note in
`docs/LAST_PLAN_EXECUTED.md` explaining why it must not become `await.Request` still
holds — it is a multi-event iterator, not a one-shot request.

The `initialize`/`onUpgradeNeeded`/`onOpenExistingDB` lifecycle machinery in `adapter.go`
is untouched.

`storage.Query` is not extended and **no vector-specific API is added to this driver**.
This driver learns to carry bytes; it learns nothing about embeddings, similarity or
search. Decision **D2** puts vectors in shard blobs, so `vectordb` reads them through the
ordinary `ReadAll` path and does its own maths. A `similaritySearch` method on this
adapter would be a responsibility violation and is explicitly rejected.

`FieldIntSlice`, `FieldStruct` and `FieldStructSlice` remain unsupported. This plan adds
`FieldBlob` only. The `default` branches added in §2 will make the others fail loudly
instead of silently, which is an improvement, but implementing them is out of scope.

## Changes

### 0. Verify `jsvalue.ScanValue` first — this gates everything

```bash
go doc webtyp.com/jsvalue ScanValue
grep -rn "Uint8Array\|CopyBytesToGo" "$(go env GOMODCACHE)"/webtyp.com/jsvalue*/
```

- If it already scans a `Uint8Array` into `*[]byte`: `mapResult` needs no change.
- If it does not: either extend `jsvalue` (preferred — it is the codec's job) or handle
  `FieldBlob` in `mapResult` before delegating. Decide and record the decision in the
  commit message.

Do not start §1 before this is answered.

### 1. `execute.go` — a JS-value encoder that knows about bytes

Replace the direct `map[string]any` → `Call` handoff with an explicit encoder.
`js.CopyBytesToJS` is the only bulk-copy primitive available, and TinyGo implements it:

```go
// toJSValue converts a Go column value into a JS value safe to hand to
// IndexedDB. It exists because js.ValueOf PANICS on []byte, and a panic
// under TinyGo wasm is unrecoverable (no recover() on this target) — so
// every value crossing the boundary is converted here, never implicitly.
func toJSValue(v any) (js.Value, error) {
	switch x := v.(type) {
	case []byte:
		// Structured clone stores a Uint8Array as binary: no JSON, no base64.
		arr := js.Global().Get("Uint8Array").New(len(x))
		if len(x) > 0 {
			js.CopyBytesToJS(arr, x)
		}
		return arr, nil
	case string, int, int64, float64, bool, nil:
		return js.ValueOf(x), nil
	default:
		return js.Value{}, fmt.Err("indexdb: unsupported column type", fmt.Sprintf("%T", v))
	}
}
```

`create` then builds a `js.Value` object explicitly instead of a `map[string]any`:

```go
data := js.Global().Get("Object").New()
for i, col := range q.Columns {
	jsVal, err := toJSValue(q.Values[i])
	if err != nil {
		return err
	}
	data.Set(col, jsVal)
}
req := store.Call("add", data)
```

The `default` branch turning a crash into an `error` is the substantive win here,
independent of vectors.

### 2. `execute.go` + `adapter.go` — `FieldBlob` in all four switches

Add to each of the four `switch f.Type.Storage()` blocks listed in **Why**:

```go
case FieldBlob:
	// jsVal is a Uint8Array; copy it back into Go linear memory.
	n := jsVal.Get("length").Int()
	b := make([]byte, n)
	if n > 0 {
		js.CopyBytesToGo(b, jsVal)
	}
	data[f.Name] = b   // or: *p = b, in the Scan variants
```

and, in the same edit, give every one of those switches a `default` that returns
`fmt.Err("indexdb: unsupported field type", f.Type.Storage().String(), "in column", f.Name)`.
Silently dropping a column is how the update-erases-the-vector bug stayed invisible.

In `checkCondition`, add `js.TypeObject` handling:

```go
// A Uint8Array is an object, so the scalar comparisons below cannot apply.
// Blobs support equality and inequality only — never <, >, LIKE or IN.
```

Implement `=` and `!=` by byte comparison, and return an error (not `false`) for an
ordering or `LIKE` operator against a blob column, so a nonsensical query is reported
instead of silently returning nothing.

### 3. `adapter.go` — implement `storage.TxExecutor`

`storage.TxExecutor`/`TxBoundExecutor` already exist and are type-asserted by callers.
Implement them so a batch is one IndexedDB transaction:

```go
// BeginTx opens ONE IndexedDB transaction spanning every declared object store
// and returns an executor bound to it. IndexedDB auto-commits a transaction as
// soon as the event loop yields with no pending request, so the bound executor
// must issue its requests back-to-back and must not await anything unrelated.
func (d *adapter) BeginTx() (storage.TxBoundExecutor, error)
```

The bound executor holds the `tx` and resolves `objectStore(name)` from it instead of
calling `getStore` (which would open a second transaction). `Commit` awaits the
transaction's `complete` event; `Rollback` calls `tx.abort()`.

The auto-commit constraint is the hard part and must be called out in the doc comment:
**anything that yields to the event loop between two requests ends the transaction.**

### 4. `execute.go` — `getStore` gains a transaction-aware sibling

Extract the store lookup so both paths share the existence pre-check:

```go
func (d *adapter) storeFrom(tx js.Value, table string) (js.Value, error)
func (d *adapter) getStore(table, mode string) (js.Value, error) // opens its own tx, unchanged
```

## Tests

All under `//go:build wasm` in `tests/`, run in a real browser via `gotest -tinygo`.
Add `tests/blob_test.go`:

| Test | Asserts |
|---|---|
| `TestBlob_RoundTripExact` | 1536 bytes (a 384-dim vector) round-trip byte for byte, including interior `0x00` and a trailing `0xFF` |
| `TestBlob_EmptyAndNil` | an empty blob and an absent blob both read back as `nil`, matching `storage.ScanAny` |
| `TestBlob_UpdateOtherColumnPreservesBlob` | **the regression test for the erasure bug**: update a text column, assert the vector is intact |
| `TestBlob_UpdateReplacesBytes` | overwriting with equal-length different bytes leaves no trace of the old value |
| `TestBlob_EqCondition` | `Where("vec").Eq(bytes)` matches by content |
| `TestBlob_OrderByBlobIsError` | ordering by a blob column returns an error rather than an empty result |
| `TestUnsupportedType_IsErrorNotPanic` | a `FieldIntSlice` column returns an error — proving the `default` branches work and nothing panics |
| `TestTx_BatchInsertOneTransaction` | 1024 rows in one `BeginTx`/`Commit`; all present afterwards |
| `TestTx_RollbackDiscards` | 1024 rows then `Rollback` leaves the store empty |
| `TestTx_LargeShardBlob` | a single 4 MB blob (1024 × 384 × 4 bytes) round-trips — the real shard size from decision D2 |

`tests/conformance_test.go` picks up the new `storage` clauses automatically; the
factory must declare the new `conformance.Embedding` record as an object store alongside
`Widget`.

## Acceptance checklist

```bash
grep -n "func toJSValue" execute.go              # → 1 match
grep -c "case FieldBlob" execute.go adapter.go   # → 4 total across both files
grep -c "default:" execute.go                    # → every Storage() switch has one
grep -n "func (d \*adapter) BeginTx" adapter.go  # → 1 match
grep -n "js.ValueOf(q.Values" execute.go         # → empty: no unchecked conversion remains
grep -n "processCursorRequest" tx.go             # → unchanged, still present
GOOS=js GOARCH=wasm go build ./...
gotest -tinygo
```

## Performance note for the reader

After this plan, a 384-dim vector costs 1536 bytes in IndexedDB and one `memcpy` in each
direction. The rejected alternative — `[]any` through `js.ValueOf` — costs 384 boxed
allocations and roughly 3 KB of JS heap per vector. That ratio is the whole reason this
plan exists; see master index **D1**.
