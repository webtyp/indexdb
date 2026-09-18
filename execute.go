//go:build wasm

package indexdb

import (
	"bytes"
	"sort"
	"syscall/js"

	"webtyp.com/await"
	"webtyp.com/jsvalue"

	"webtyp.com/fmt"
	. "webtyp.com/model"
	"webtyp.com/storage"
)

type storeGetter func(table, mode string) (js.Value, error)

func (d *adapter) executeWithStore(getStore storeGetter, q storage.Query, m Model, factory func() Model, each func(Model), eachJS func(js.Value)) error {
	switch q.Action {
	case storage.ActionCreate:
		return d.create(getStore, q, m)
	case storage.ActionUpdate:
		return d.update(getStore, q, m)
	case storage.ActionDelete:
		return d.delete(getStore, q, m)
	case storage.ActionReadOne:
		return d.readOne(getStore, q, m)
	case storage.ActionReadAll:
		return d.readAll(getStore, q, factory, each, eachJS)
	default:
		return fmt.Err("Action not implemented")
	}
}

// execute implements storage.Adapter for IndexDB.
func (d *adapter) execute(q storage.Query, m Model, factory func() Model, each func(Model), eachJS func(js.Value)) error {
	return d.executeWithStore(d.getStore, q, m, factory, each, eachJS)
}

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

func (d *adapter) create(getStore storeGetter, q storage.Query, m Model) error {
	// Establish a "readwrite" transaction block directed at the store mapped via q.Table.
	store, err := getStore(q.Table, "readwrite")
	if err != nil {
		return err
	}

	// Iterate structurally mapping q.Columns and q.Values onto a conventional JavaScript Object
	data := js.Global().Get("Object").New()
	for i, col := range q.Columns {
		jsVal, err := toJSValue(q.Values[i])
		if err != nil {
			return err
		}
		data.Set(col, jsVal)
	}

	// Deploy store.add() and explicitly await its resolution event.
	req := store.Call("add", data)
	_, err = await.Request(req)
	return err
}

func (d *adapter) update(getStore storeGetter, q storage.Query, m Model) error {
	store, err := getStore(q.Table, "readwrite")
	if err != nil {
		return err
	}

	fields := m.Schema()
	pkName := ""
	for _, f := range fields {
		if f.IsPK() {
			pkName = f.Name
			break
		}
	}

	// Optimize: single PK equality condition (handles updates with direct get and put)
	if len(q.Conditions) == 1 && q.Conditions[0].Operator() == "=" && q.Conditions[0].Field() == pkName {
		pkValue := q.Conditions[0].Value()
		jsKey, err := toJSValue(pkValue)
		if err != nil {
			return err
		}
		getReq := store.Call("get", jsKey)
		val, err := await.Request(getReq)
		if err != nil {
			return err
		}

		if !val.Truthy() || val.IsUndefined() {
			return storage.ErrNoRows
		}

		// Overwrite fields
		data := js.Global().Get("Object").New()
		for _, f := range fields {
			jsVal := val.Get(f.Name)
			if !jsVal.IsUndefined() {
				switch f.Type.Storage() {
				case FieldText, FieldRaw:
					data.Set(f.Name, jsVal.String())
				case FieldInt:
					data.Set(f.Name, int64(jsVal.Int()))
				case FieldFloat:
					data.Set(f.Name, jsVal.Float())
				case FieldBool:
					data.Set(f.Name, jsVal.Bool())
				case FieldBlob:
					n := jsVal.Get("length").Int()
					b := make([]byte, n)
					if n > 0 {
						js.CopyBytesToGo(b, jsVal)
					}
					arr, err := toJSValue(b)
					if err != nil {
						return err
					}
					data.Set(f.Name, arr)
				default:
					return fmt.Err("indexdb: unsupported field type", f.Type.Storage().String(), "in column", f.Name)
				}
			}
		}

		for i, col := range q.Columns {
			if col == pkName {
				newVal := q.Values[i]
				if newVal == "" || newVal == nil || newVal == int(0) || newVal == int64(0) || newVal == float64(0) {
					continue
				}
			}
			jsVal, err := toJSValue(q.Values[i])
			if err != nil {
				return err
			}
			data.Set(col, jsVal)
		}

		putReq := store.Call("put", data)
		_, err = await.Request(putReq)
		return err
	}

	// For cursors, collect all matching records first to avoid nested AwaitRequest deadlocks
	type matchRecord struct {
		val js.Value
	}
	var matched []matchRecord

	req := store.Call("openCursor")
	var condErr error
	err = processCursorRequest(req, func(cursor js.Value) bool {
		val := cursor.Get("value")
		match, err := checkConditions(val, q.Conditions)
		if err != nil {
			condErr = err
			return false
		}
		if match {
			matched = append(matched, matchRecord{val: val})
		}
		return true
	})
	if err != nil {
		return err
	}
	if condErr != nil {
		return condErr
	}

	for _, item := range matched {
		data := js.Global().Get("Object").New()
		for _, f := range fields {
			jsVal := item.val.Get(f.Name)
			if !jsVal.IsUndefined() {
				switch f.Type.Storage() {
				case FieldText, FieldRaw:
					data.Set(f.Name, jsVal.String())
				case FieldInt:
					data.Set(f.Name, int64(jsVal.Int()))
				case FieldFloat:
					data.Set(f.Name, jsVal.Float())
				case FieldBool:
					data.Set(f.Name, jsVal.Bool())
				case FieldBlob:
					n := jsVal.Get("length").Int()
					b := make([]byte, n)
					if n > 0 {
						js.CopyBytesToGo(b, jsVal)
					}
					arr, err := toJSValue(b)
					if err != nil {
						return err
					}
					data.Set(f.Name, arr)
				default:
					return fmt.Err("indexdb: unsupported field type", f.Type.Storage().String(), "in column", f.Name)
				}
			}
		}

		for i, col := range q.Columns {
			if col == pkName {
				newVal := q.Values[i]
				if newVal == "" || newVal == nil || newVal == int(0) || newVal == int64(0) || newVal == float64(0) {
					continue
				}
			}
			jsVal, err := toJSValue(q.Values[i])
			if err != nil {
				return err
			}
			data.Set(col, jsVal)
		}

		putReq := store.Call("put", data)
		_, err = await.Request(putReq)
		if err != nil {
			return err
		}
	}

	return nil
}

func (d *adapter) delete(getStore storeGetter, q storage.Query, m Model) error {
	store, err := getStore(q.Table, "readwrite")
	if err != nil {
		return err
	}

	fields := m.Schema()
	pkName := ""
	for _, f := range fields {
		if f.IsPK() {
			pkName = f.Name
			break
		}
	}

	// If it is a simple single equality condition on the PK, we can delete by key directly.
	if len(q.Conditions) == 1 && q.Conditions[0].Operator() == "=" && q.Conditions[0].Field() == pkName {
		pkValue := q.Conditions[0].Value()
		jsKey, err := toJSValue(pkValue)
		if err != nil {
			return err
		}
		req := store.Call("delete", jsKey)
		_, err = await.Request(req)
		return err
	}

	// Otherwise, find matching records using a cursor and delete them.
	req := store.Call("openCursor")
	var condErr error

	err = processCursorRequest(req, func(cursor js.Value) bool {
		val := cursor.Get("value")

		match, err := checkConditions(val, q.Conditions)
		if err != nil {
			condErr = err
			return false
		}

		if match {
			cursor.Call("delete")
		}

		return true
	})
	if err != nil {
		return err
	}
	return condErr
}

func (d *adapter) readOne(getStore storeGetter, q storage.Query, m Model) error {
	store, err := getStore(q.Table, "readonly")
	if err != nil {
		return err
	}

	fields := m.Schema()
	pkName := ""
	for _, f := range fields {
		if f.IsPK() {
			pkName = f.Name
			break
		}
	}

	// Attempt to get by key if simple condition on the PK.
	if len(q.Conditions) == 1 && q.Conditions[0].Operator() == "=" && q.Conditions[0].Field() == pkName {
		key := q.Conditions[0].Value()
		jsKey, err := toJSValue(key)
		if err == nil {
			req := store.Call("get", jsKey)
			result, err := await.Request(req)
			if err == nil && result.Truthy() && !result.IsUndefined() {
				return mapResult(result, m)
			}
		}
		// If not found by key, fall back to cursor.
	}

	// Otherwise, iterate with cursor until first match
	req := store.Call("openCursor")
	var found bool
	var condErr error

	err = processCursorRequest(req, func(cursor js.Value) bool {
		val := cursor.Get("value")

		// Check conditions
		match, err := checkConditions(val, q.Conditions)
		if err != nil {
			condErr = err
			return false
		}

		if match {
			// Found it
			err := mapResult(val, m)
			if err != nil {
				d.logger("Mapping error:", err)
			}
			found = true
			return false // Stop iteration
		}

		return true // Continue iteration
	})

	if err != nil {
		return err
	}
	if condErr != nil {
		return condErr
	}
	if !found {
		return storage.ErrNoRows
	}
	return nil
}

type matchedItem struct {
	model Model
	val   js.Value
}

func (d *adapter) readAll(getStore storeGetter, q storage.Query, factory func() Model, each func(Model), eachJS func(js.Value)) error {
	store, err := getStore(q.Table, "readonly")
	if err != nil {
		return err
	}

	req := store.Call("openCursor")

	var matched []matchedItem
	var condErr error

	err = processCursorRequest(req, func(cursor js.Value) bool {
		val := cursor.Get("value")

		match, err := checkConditions(val, q.Conditions)
		if err != nil {
			condErr = err
			return false
		}

		if match {
			var newItem Model
			if factory != nil {
				newItem = factory()
				if newItem != nil {
					err := mapResult(val, newItem)
					if err != nil {
						d.logger("Mapping error:", err)
						return true // Continue iteration
					}
				}
			}
			matched = append(matched, matchedItem{model: newItem, val: val})
		}

		return true // Continue iteration
	})
	if err != nil {
		return err
	}
	if condErr != nil {
		return condErr
	}

	// Apply OrderBy
	if len(q.OrderBy) > 0 {
		var sample Model
		if factory != nil {
			sample = factory()
		} else if len(matched) > 0 {
			sample = matched[0].model
		}
		if sample != nil {
			for _, f := range sample.Schema() {
				for _, order := range q.OrderBy {
					if f.Name == order.Column() {
						switch f.Type.Storage() {
						case FieldBlob:
							return fmt.Err("indexdb: order by blob column not supported")
						}
					}
				}
			}
		}
		for _, order := range q.OrderBy {
			col := order.Column()
			for _, item := range matched {
				jsVal := item.val.Get(col)
				if jsVal.Type() == js.TypeObject && !jsVal.IsNull() && !jsVal.Get("length").IsUndefined() {
					return fmt.Err("indexdb: order by blob column not supported")
				}
			}
		}

		sort.Slice(matched, func(i, j int) bool {
			for _, order := range q.OrderBy {
				col := order.Column()
				jsA := matched[i].val.Get(col)
				jsB := matched[j].val.Get(col)

				// Compare jsA and jsB
				switch jsA.Type() {
				case js.TypeString:
					strA := jsA.String()
					strB := jsB.String()
					if strA != strB {
						if order.Dir() == "DESC" {
							return strA > strB
						}
						return strA < strB
					}
				case js.TypeNumber:
					numA := jsA.Float()
					numB := jsB.Float()
					if numA != numB {
						if order.Dir() == "DESC" {
							return numA > numB
						}
						return numA < numB
					}
				case js.TypeBoolean:
					boolA := jsA.Bool()
					boolB := jsB.Bool()
					if boolA != boolB {
						if order.Dir() == "DESC" {
							return boolA && !boolB
						}
						return !boolA && boolB
					}
				}
			}
			return false
		})
	}

	// Apply Offset and Limit
	start := q.Offset
	if start < 0 {
		start = 0
	}
	if start > len(matched) {
		start = len(matched)
	}

	end := len(matched)
	if q.Limit > 0 {
		end = start + q.Limit
		if end > len(matched) {
			end = len(matched)
		}
	}

	sliced := matched[start:end]

	// Output results
	for _, item := range sliced {
		if each != nil {
			each(item.model)
		} else if eachJS != nil {
			eachJS(item.val)
		}
	}

	return nil
}

// mapResult maps a JS value to a Model's pointers
func mapResult(val js.Value, m Model) error {
	fields := m.Schema()
	ptrs := m.Pointers()

	for i, field := range fields {
		jsVal := val.Get(field.Name)
		if jsVal.IsUndefined() {
			continue
		}

		if err := jsvalue.ScanValue(jsVal, ptrs[i]); err != nil {
			return err
		}
	}
	return nil
}

// checkConditions checks a slice of conditions sequentially
func checkConditions(val js.Value, conditions []storage.Condition) (bool, error) {
	if len(conditions) == 0 {
		return true, nil
	}

	cond := conditions[0]
	fieldVal := val.Get(cond.Field())
	match, err := checkCondition(fieldVal, cond)
	if err != nil {
		return false, err
	}

	for i := 1; i < len(conditions); i++ {
		cond = conditions[i]
		fieldVal = val.Get(cond.Field())
		condMatch, err := checkCondition(fieldVal, cond)
		if err != nil {
			return false, err
		}
		if cond.Logic() == "OR" {
			match = match || condMatch
		} else {
			match = match && condMatch
		}
	}

	return match, nil
}

// checkCondition checks if a JS value satisfies a condition
func checkCondition(val js.Value, cond storage.Condition) (bool, error) {
	// A Uint8Array is an object, so scalar comparisons below cannot apply.
	// Blobs support equality and inequality only — never <, >, LIKE or IN.
	if val.Type() == js.TypeObject && !val.IsNull() {
		lenProp := val.Get("length")
		if !lenProp.IsUndefined() {
			condVal := cond.Value()
			var b2 []byte
			switch cv := condVal.(type) {
			case []byte:
				b2 = cv
			case nil:
				b2 = nil
			default:
				return false, fmt.Err("indexdb: invalid condition value for blob column")
			}

			n := lenProp.Int()
			b1 := make([]byte, n)
			if n > 0 {
				js.CopyBytesToGo(b1, val)
			}

			switch cond.Operator() {
			case "=":
				return bytes.Equal(b1, b2), nil
			case "!=":
				return !bytes.Equal(b1, b2), nil
			default:
				return false, fmt.Err("indexdb: operator", cond.Operator(), "not supported on blob column")
			}
		}
	}

	var goVal any
	switch val.Type() {
	case js.TypeString:
		goVal = val.String()
	case js.TypeNumber:
		goVal = val.Float()
	case js.TypeBoolean:
		goVal = val.Bool()
	default:
		return false, nil
	}

	condVal := cond.Value()

	switch cond.Operator() {
	case "=":
		return compareAny(goVal, condVal), nil
	case "!=":
		return !compareAny(goVal, condVal), nil
	case "IN":
		return valueInList(goVal, condVal), nil
	case "LIKE":
		sVal, okS := goVal.(string)
		patVal, okP := condVal.(string)
		if okS && okP {
			return matchLike(sVal, patVal), nil
		}
		return false, nil
	case ">":
		if v1, ok := goVal.(float64); ok {
			if v2, ok := condVal.(float64); ok {
				return v1 > v2, nil
			}
			if v2, ok := condVal.(int); ok {
				return v1 > float64(v2), nil
			}
			if v2, ok := condVal.(int64); ok {
				return v1 > float64(v2), nil
			}
		} else if v1, ok := goVal.(string); ok {
			if v2, ok := condVal.(string); ok {
				return v1 > v2, nil
			}
		}
	case ">=":
		if v1, ok := goVal.(float64); ok {
			if v2, ok := condVal.(float64); ok {
				return v1 >= v2, nil
			}
			if v2, ok := condVal.(int); ok {
				return v1 >= float64(v2), nil
			}
			if v2, ok := condVal.(int64); ok {
				return v1 >= float64(v2), nil
			}
		} else if v1, ok := goVal.(string); ok {
			if v2, ok := condVal.(string); ok {
				return v1 >= v2, nil
			}
		}
	case "<":
		if v1, ok := goVal.(float64); ok {
			if v2, ok := condVal.(float64); ok {
				return v1 < v2, nil
			}
			if v2, ok := condVal.(int); ok {
				return v1 < float64(v2), nil
			}
			if v2, ok := condVal.(int64); ok {
				return v1 < float64(v2), nil
			}
		} else if v1, ok := goVal.(string); ok {
			if v2, ok := condVal.(string); ok {
				return v1 < v2, nil
			}
		}
	case "<=":
		if v1, ok := goVal.(float64); ok {
			if v2, ok := condVal.(float64); ok {
				return v1 <= v2, nil
			}
			if v2, ok := condVal.(int); ok {
				return v1 <= float64(v2), nil
			}
			if v2, ok := condVal.(int64); ok {
				return v1 <= float64(v2), nil
			}
		} else if v1, ok := goVal.(string); ok {
			if v2, ok := condVal.(string); ok {
				return v1 <= v2, nil
			}
		}
	}

	return false, nil
}

func compareAny(a, b any) bool {
	if a == b {
		return true
	}
	var fA, fB float64
	var okA, okB bool
	switch v := a.(type) {
	case float64:
		fA, okA = v, true
	case int64:
		fA, okA = float64(v), true
	case int:
		fA, okA = float64(v), true
	}
	switch v := b.(type) {
	case float64:
		fB, okB = v, true
	case int64:
		fB, okB = float64(v), true
	case int:
		fB, okB = float64(v), true
	}
	if okA && okB {
		return fA == fB
	}
	return false
}

func valueInList(goVal any, list any) bool {
	switch l := list.(type) {
	case []any:
		for _, item := range l {
			if compareAny(goVal, item) {
				return true
			}
		}
	case []string:
		for _, item := range l {
			if compareAny(goVal, item) {
				return true
			}
		}
	case []int64:
		for _, item := range l {
			if compareAny(goVal, item) {
				return true
			}
		}
	case []int:
		for _, item := range l {
			if compareAny(goVal, item) {
				return true
			}
		}
	case []float64:
		for _, item := range l {
			if compareAny(goVal, item) {
				return true
			}
		}
	}
	return false
}

func matchLike(s, pattern string) bool {
	if len(pattern) == 0 {
		return s == ""
	}
	if pattern == "%" {
		return true
	}

	hasPrefixWildcard := pattern[0] == '%'
	hasSuffixWildcard := pattern[len(pattern)-1] == '%'

	cleanPattern := pattern
	if hasPrefixWildcard {
		cleanPattern = cleanPattern[1:]
	}
	if hasSuffixWildcard {
		cleanPattern = cleanPattern[:len(cleanPattern)-1]
	}

	if hasPrefixWildcard && hasSuffixWildcard {
		return containsSubstring(s, cleanPattern)
	}
	if hasPrefixWildcard {
		return hasSuffix(s, cleanPattern)
	}
	if hasSuffixWildcard {
		return hasPrefix(s, cleanPattern)
	}
	return s == pattern
}

func containsSubstring(s, sub string) bool {
	if len(sub) == 0 {
		return true
	}
	if len(s) < len(sub) {
		return false
	}
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

func hasSuffix(s, suffix string) bool {
	return len(s) >= len(suffix) && s[len(s)-len(suffix):] == suffix
}
