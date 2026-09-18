---
PLAN: "feat: columnas binarias (blob) y transacciones por lote"
TAG: v0.2.0
EXECUTOR: unassigned
REVIEWER: none
STATUS: review
SESSION: 17791316637842426773
PR: https://github.com/webtyp/indexdb/pull/17
---

> Parte del esfuerzo de búsqueda semántica nativa en el navegador. Índice maestro:
> https://github.com/webtyp/agent/blob/main/docs/PLAN.md — las decisiones **D1** y **D2** de
> ahí son la justificación de la representación elegida abajo.
>
> **Nota de idioma:** la prosa va en español; los bloques de código mantienen sus
> comentarios en inglés, como el resto del código fuente de este repositorio.

# Plan — que IndexedDB pueda transportar un vector

## Por qué

Este driver no puede guardar datos binarios, y el modo de falla es el peor disponible.

`create` (`execute.go:44-49`) arma un `map[string]any` a partir de `q.Columns`/`q.Values` y
se lo entrega a `store.Call("add", data)`. `Call` convierte los argumentos con `js.ValueOf`,
que soporta `[]any` pero **no `[]byte`** y **no `[]float32`** — hace pánico con cualquier
otra cosa. `getStore` (`tx.go`) ya documenta la consecuencia en este mismo repositorio:

> Calling `transaction()` with an unknown store throws a NotFoundError, which is
> unrecoverable under TinyGo wasm (`recover()` not supported on this target).

Lo mismo aplica acá: un pánico de `js.ValueOf` sobre una columna blob es un **crash
irrecuperable de toda la aplicación**, no un retorno de error. Así que hoy una columna
`model.Blob()` no solamente falla en persistirse — se lleva puesta la página.

Otros cuatro caminos tienen la misma forma. Cada uno despacha sobre `f.Type.Storage()` con
un `switch` que cubre `FieldText`/`FieldInt`/`FieldFloat`/`FieldBool` y descarta
silenciosamente todo lo demás:

| Ubicación | Efecto sobre una columna blob |
|---|---|
| `execute.go:update` (camino rápido de PK, ~línea 88) | el campo se descarta de `data`, así que actualizar cualquier otra columna **borra el vector** |
| `execute.go:update` (camino de cursor, ~línea 130) | mismo borrado |
| `adapter.go:simpleRows.Scan` (~línea 147) | el blob se saltea; el destino conserva su valor anterior |
| `execute.go:checkCondition` | `val.Type()` es `js.TypeObject` para un `Uint8Array` → `return false`, así que un blob nunca coincide con ninguna condición, incluido `IS NOT NULL` |

`mapResult` delega en `jsvalue.ScanValue`, cuyo soporte de `Uint8Array` está **sin verificar
y es la primera tarea de abajo**.

Aparte: `getStore` abre una **transacción nueva por llamada**, así que insertar un conjunto
de shard de 1024 filas cuesta 1024 transacciones. Las transacciones de IndexedDB
auto-confirman cuando el event loop cede, así que esto no es solo lento: es no-atómico, y
una falla a mitad de camino deja el store en un estado partido sin forma de revertir.

## Lo que NO cambia

`processCursorRequest` (`tx.go`) queda exactamente como está. La nota en
`docs/LAST_PLAN_EXECUTED.md` que explica por qué no debe convertirse en `await.Request`
sigue vigente — es un iterador multi-evento, no un request de un solo disparo.

La maquinaria de ciclo de vida `initialize`/`onUpgradeNeeded`/`onOpenExistingDB` de
`adapter.go` queda intacta.

`storage.Query` no se extiende y **no se agrega ninguna API específica de vectores a este
driver**. Este driver aprende a transportar bytes; no aprende nada sobre embeddings,
similitud ni búsqueda. La decisión **D2** pone los vectores en blobs de shard, así que
`vectordb` los lee por el camino ordinario de `ReadAll` y hace su propia matemática. Un
método `similaritySearch` en este adaptador sería una violación de responsabilidad y queda
explícitamente rechazado.

`FieldIntSlice`, `FieldStruct` y `FieldStructSlice` siguen sin soporte. Este plan agrega
solamente `FieldBlob`. Las ramas `default` que agrega la §2 van a hacer que los demás fallen
ruidosamente en vez de en silencio, lo cual es una mejora, pero implementarlos queda fuera
de alcance.

## Cambios

### 0. Verificar `jsvalue.ScanValue` primero — esto condiciona todo lo demás

```bash
go doc webtyp.com/jsvalue ScanValue
grep -rn "Uint8Array\|CopyBytesToGo" "$(go env GOMODCACHE)"/webtyp.com/jsvalue*/
```

- Si ya escanea un `Uint8Array` hacia `*[]byte`: `mapResult` no necesita cambios.
- Si no lo hace: o extender `jsvalue` (preferido — es el trabajo del códec) o manejar
  `FieldBlob` en `mapResult` antes de delegar. Decidir y registrar la decisión en el mensaje
  del commit.

No arrancar la §1 antes de tener esto respondido.

### 1. `execute.go` — un encoder de valores JS que sepa de bytes

Reemplazar la entrega directa `map[string]any` → `Call` por un encoder explícito.
`js.CopyBytesToJS` es la única primitiva de copia masiva disponible, y TinyGo la implementa:

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

`create` entonces construye un objeto `js.Value` explícitamente en vez de un
`map[string]any`:

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

La rama `default` que convierte un crash en un `error` es la ganancia sustantiva acá,
independientemente de los vectores.

### 2. `execute.go` + `adapter.go` — `FieldBlob` en los cuatro switches

Agregar a cada uno de los cuatro bloques `switch f.Type.Storage()` listados en **Por qué**:

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

y, en la misma edición, darle a cada uno de esos switches un `default` que devuelva
`fmt.Err("indexdb: unsupported field type", f.Type.Storage().String(), "in column", f.Name)`.
Descartar una columna en silencio es cómo el bug de "update borra el vector" se mantuvo
invisible.

En `checkCondition`, agregar manejo de `js.TypeObject`:

```go
// A Uint8Array is an object, so the scalar comparisons below cannot apply.
// Blobs support equality and inequality only — never <, >, LIKE or IN.
```

Implementar `=` y `!=` por comparación de bytes, y devolver un error (no `false`) para un
operador de orden o `LIKE` contra una columna blob, para que una consulta sin sentido se
reporte en vez de devolver nada en silencio.

### 3. `adapter.go` — implementar `storage.TxExecutor`

`storage.TxExecutor`/`TxBoundExecutor` ya existen y los llamadores les hacen type assertion.
Implementalos para que un lote sea una sola transacción de IndexedDB:

```go
// BeginTx opens ONE IndexedDB transaction spanning every declared object store
// and returns an executor bound to it. IndexedDB auto-commits a transaction as
// soon as the event loop yields with no pending request, so the bound executor
// must issue its requests back-to-back and must not await anything unrelated.
func (d *adapter) BeginTx() (storage.TxBoundExecutor, error)
```

El executor ligado sostiene la `tx` y resuelve `objectStore(name)` desde ella en vez de
llamar a `getStore` (que abriría una segunda transacción). `Commit` espera el evento
`complete` de la transacción; `Rollback` llama a `tx.abort()`.

La restricción de auto-commit es la parte difícil y tiene que quedar señalada en el
comentario de doc: **cualquier cosa que ceda al event loop entre dos requests termina la
transacción.**

### 4. `execute.go` — `getStore` gana un hermano consciente de transacciones

Extraer la búsqueda del store para que ambos caminos compartan la verificación previa de
existencia:

```go
func (d *adapter) storeFrom(tx js.Value, table string) (js.Value, error)
func (d *adapter) getStore(table, mode string) (js.Value, error) // opens its own tx, unchanged
```

## Tests

Todos bajo `//go:build wasm` en `tests/`, ejecutados en un navegador real vía
`gotest -tinygo`. Agregar `tests/blob_test.go`:

| Test | Verifica |
|---|---|
| `TestBlob_RoundTripExact` | 1536 bytes (un vector de 384 dims) hacen round-trip byte a byte, incluyendo `0x00` interior y un `0xFF` final |
| `TestBlob_EmptyAndNil` | un blob vacío y un blob ausente se leen ambos como `nil`, coincidiendo con `storage.ScanAny` |
| `TestBlob_UpdateOtherColumnPreservesBlob` | **el test de regresión del bug de borrado**: actualizar una columna de texto y verificar que el vector queda intacto |
| `TestBlob_UpdateReplacesBytes` | sobrescribir con bytes distintos del mismo largo no deja rastro del valor anterior |
| `TestBlob_EqCondition` | `Where("vec").Eq(bytes)` coincide por contenido |
| `TestBlob_OrderByBlobIsError` | ordenar por una columna blob devuelve error en vez de un resultado vacío |
| `TestUnsupportedType_IsErrorNotPanic` | una columna `FieldIntSlice` devuelve error — demostrando que las ramas `default` funcionan y nada hace pánico |
| `TestTx_BatchInsertOneTransaction` | 1024 filas en un `BeginTx`/`Commit`; todas presentes después |
| `TestTx_RollbackDiscards` | 1024 filas y después `Rollback` deja el store vacío |
| `TestTx_LargeShardBlob` | un único blob de 4 MB (1024 × 384 × 4 bytes) hace round-trip — el tamaño real de shard de la decisión D2 |

`tests/conformance_test.go` recoge las cláusulas nuevas de `storage` automáticamente; la
factory tiene que declarar el nuevo record `conformance.Embedding` como object store junto a
`Widget`.

## Checklist de aceptación

```bash
grep -n "func toJSValue" execute.go              # → 1 coincidencia
grep -c "case FieldBlob" execute.go adapter.go   # → 4 en total entre ambos archivos
grep -c "default:" execute.go                    # → cada switch sobre Storage() tiene uno
grep -n "func (d \*adapter) BeginTx" adapter.go  # → 1 coincidencia
grep -n "js.ValueOf(q.Values" execute.go         # → vacío: no queda ninguna conversión sin verificar
grep -n "processCursorRequest" tx.go             # → sin cambios, sigue presente
GOOS=js GOARCH=wasm go build ./...
gotest -tinygo
```

## Nota de rendimiento para quien lea

Después de este plan, un vector de 384 dims cuesta 1536 bytes en IndexedDB y un `memcpy` en
cada dirección. La alternativa rechazada — `[]any` a través de `js.ValueOf` — cuesta 384
allocations boxeadas y alrededor de 3 KB de heap JS por vector. Esa relación es toda la
razón por la que este plan existe; ver **D1** del índice maestro.
