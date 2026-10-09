---
PLAN: "feat!: typed New(name, models...) (storage.Conn, error) — no IDGenerator, no logger, missing stores created on upgrade"
TAG: v0.7.0
EXECUTOR: jules
REVIEWER: none
---

> This plan is dispatched via the CodeJob workflow. See skill: agents-workflow.

# Plan — `indexdb`: typed constructor and schema upgrade

## 0. Context (read first)

`webtyp.com/indexdb` is the IndexedDB backend of `webtyp.com/storage` (`storage.Conn` +
`storage.TxExecutor`). An offline-first wave (master plan, in Spanish:
<https://github.com/veltylabs/mjosefa-cms/blob/main/docs/OFFLINE_FIRST_MASTER_PLAN.md>) makes it
the **local database of every browser** of a clinic app: the same domain modules that run on
the server run in the page over this backend, and a sync library replicates them. It is
injected only at the app's composition root; libraries receive a `storage.Conn`.

Defects in the current code (verified on `main`, v0.6.2):

1. `New(dbName string, idg IDGenerator, logger func(...any), structTables ...any) storage.Conn`
   - `structTables ...any`: anything compiles; a value that is not a `model.Model` is skipped at
     runtime with a log line (`"does not implement Model interface, skipping"`).
   - `idg IDGenerator`: a storage driver must not mint ids (callers do). The only use is
     `getNewID()` in `adapter.go`, **which nothing calls** — dead code.
   - `logger func(...any)`: failures to open or create stores are logged and swallowed; `New`
     returns a `storage.Conn` even when the database could not be opened.
2. **No schema upgrade.** `indexedDB.open(name)` is called without a version, so
   `onupgradeneeded` fires only when the database is first created. When a new app version
   declares a new model, an existing browser never gets that object store and every query on it
   fails. A PWA that evolves breaks on its first update.

## Design gate

### 1. Prior art
- **Dexie.js**: `db.version(n).stores({...})`; adding a store requires bumping the version, and
  Dexie runs the upgrade. Explicit integer versions.
- **idb (Jake Archibald)**: `openDB(name, version, { upgrade(db, oldVersion) {...} })`; the
  caller writes the migration by hand.
- **PouchDB / RxDB**: schema declared per collection; the library creates missing stores and
  manages the IndexedDB version internally.

We follow RxDB's behaviour (the library reconciles), because the schema is already declared by
the typed models the caller passes: asking for a hand-maintained version number would be a
second source of truth that drifts. The reconciliation is **additive only**: a store that is no
longer declared is never deleted (user data must not vanish because a model was renamed).

### 2. Novice-name test
`indexdb.New("mjosefa", &Reservation{}, &Patient{})` reads as "a new IndexedDB named mjosefa
with these tables". Errors come back from `New`, like `sql.Open`/`postgres.Open`.

### 3. Complexity ledger
```
Concepts the developer must learn   −2 (IDGenerator here, logger callback)
Files they must touch to do X        0
Lines at the call site               −0 (one call, shorter)
Ways to do the same thing            0
```

### 4. Where it belongs
Store creation and upgrade are IndexedDB mechanics: this repository. Id minting belongs to the
caller (`model.IDGenerator`, injected into modules, never into a driver).

### 5. What this deletes
The `idg` and `logger` parameters, the `idGen` and `logger` fields, `getNewID()`, every
`d.logger(...)` call (each becomes a returned error or is removed, see Stage 2), the
`structTables []any` field (becomes `[]model.Model`).

## 1. Target API

```go
// New opens (creating or upgrading as needed) the IndexedDB database name and makes sure an
// object store exists for every model. Missing stores are created by reopening the database
// with version+1; existing stores and their data are never dropped.
func New(name string, models ...model.Model) (storage.Conn, error)
```

The returned value keeps implementing `storage.TxExecutor` (unchanged `BeginTx`).

## 2. Stages

### Stage 1 — signature and fields (`adapter.go`)
- Replace `New` with §1. `newAdapter(name string) *adapter`.
- `adapter.tables` becomes `[]model.Model`; delete `idGen`, `logger`, `getNewID`.
- `New` returns `nil, err` when opening fails, when there are zero models
  (`indexdb: New requires at least one model`), or when two models share a `ModelName()`
  (`indexdb: duplicate model name <name>`). Error texts are unexported typed constants
  (`type dbError string` + `Error()`), never `errors.New`.

### Stage 2 — open, then upgrade only if needed
Opening algorithm (all inside `New`, still blocking until done as today):
1. `indexedDB.open(name)` **without** a version.
   - On `upgradeneeded` (brand-new database): create every store, as today.
   - On `success`: compare `db.objectStoreNames` against the models. If every store exists →
     done. If any is missing → read `db.version`, `db.close()`, and reopen with
     `indexedDB.open(name, version+1)`; in that `upgradeneeded`, create **only** the missing
     stores (same `createTable` as today, including its indexes).
2. `blocked` event on the reopen (another tab still holds the old version): return
   `indexdb: upgrade blocked by another open tab of this app; close it and reload`.
   Also register `db.onversionchange = () => db.close()` on every connection this package opens,
   so an old tab releases the database instead of blocking the new one.
3. Every former `d.logger(...)` site in `adapter.go` and `execute.go`: in the open/upgrade path,
   return the error from `New`; in query paths, return it from the method (most already do and
   the log line was redundant — delete it). After this stage the package has no logger.

### Stage 3 — tests (`tests/`, `//go:build wasm`)
- `tests/setup_test.go`: `SetupDB(dbName string, models ...model.Model) storage.Conn` calls
  `indexdb.New` and `t.Fatal`s on error (pass `t`); delete the test `idGenerator`. Update every
  caller in `tests/`.
- `tests/conformance_test.go`: keep `storage/conformance.Run`. **Every clause must pass.** If
  any clause fails, fix the adapter (not the suite): the offline wave runs real domain queries
  here (ranges with `Gte`/`Lte`, `In`, `Or`, ordering, `Limit`/`Offset`, transactions).
- New `tests/upgrade_test.go`:
  1. `New(name, &A{})`, create a row, `Close()`.
  2. `New(name, &A{}, &B{})` → no error; the row in `A` is still readable; create + read a row
     in `B`.
  3. `New(name, &A{})` again (B no longer declared) → no error, and `B`'s store and row still
     exist (read it through a `New(name, &A{}, &B{})`).
  Use small test models declared in the test package (hand-written `ModelName`, `Schema`,
  `Pointers`, as the existing test models do).
- New case: `New(name)` with no models → error; `New(name, &A{}, &A{})` → duplicate error.

### Stage 4 — docs
`README.md` is stale (it shows a `Schema() []indexdb.Field` API and `db.Query(...)` that no
longer exist). Rewrite it: one paragraph of what the package is, the §1 signature, a 10-line
example (`conn, err := indexdb.New("app", &Patient{})`, then `db := orm.New(conn)`), the
upgrade rule (additive, automatic, the "blocked" error), and an "I want X → use Y" table.

## 3. Code rules (non-negotiable)
- WASM package: `webtyp.com/fmt` for formatting/errors; no `errors`, `strconv`, `strings`,
  `fmt` from the standard library in non-test code.
- No `any` in the public API; `any` only at the `syscall/js` edge.
- No exported symbol other than `New` (plus what already exists and is used by consumers).
- Tests only in `tests/`; never export a symbol for a test.

## 4. Acceptance criteria
- `gotest ./...` green, including the full `storage/conformance` suite in the wasm lane.
- `grep -rn "IDGenerator\|idGen\|getNewID\|logger" --include=*.go . | grep -v _test.go` → empty.
- `grep -n "func New(" adapter.go` → `func New(name string, models ...model.Model) (storage.Conn, error)`
  (the parameter type may appear as `Model` if the file dot-imports `webtyp.com/model`).

| Stage | Files | Done when |
|---|---|---|
| 1 | `adapter.go` | new signature compiles |
| 2 | `adapter.go`, `execute.go` | missing stores created on reopen; no logger |
| 3 | `tests/setup_test.go`, `tests/conformance_test.go`, `tests/upgrade_test.go`, other `tests/*` | all green |
| 4 | `README.md` | rewritten |

**Known downstream consumer** (not this plan's job): `webtyp/vectordb`'s
`vectordb_indexdb_test.go` calls the old signature; it is updated when vectordb bumps.
