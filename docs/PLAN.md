---
PLAN: "feat: indexdb honours storage.ILike"
EXECUTOR: jules
REVIEWER: none
STATUS: running
SESSION: 9294822643733930796
---

> Read AGENTS.md at the repo root before any change.
> **Finish by yourself:** when the delivery check passes, commit, push and open the pull request. Never ask whether to
> proceed or finalize — nobody answers, and a session that waits ends without your work. Commit after every stage.
> Never run `git reset`, `git restore`, `git checkout -- <file>` or `git stash` on your own work.
> **Publishing:** you publish only by OPENING THE PULL REQUEST. Submit after EACH stage that compiles.
> **Never ask a yes/no question about progress**; ask only when a signature in this plan is wrong, and name the symbol.
> **Before you submit:** `git status` must be clean and `git diff --stat main` must list the files of the plan.

# Plan: `indexdb` answers `storage.ILike`

`storage` v0.2.3 added `storage.ILike(field, value) Condition` (operator `"ILIKE"`) with a conformance clause every backend must pass. `indexdb/execute.go` matches in a `switch cond.Operator()` (≈ line 660) with `case "LIKE": … matchLike(sVal, patVal)` and `matchLike(s, pattern string) bool` (≈ line 808).

## Tools
```bash
go install webtyp.com/devflow/cmd/gotest@latest
go get webtyp.com/storage@v0.2.3
```

## Stage 1 — match (`execute.go`)
Next to `case "LIKE":` add:

```go
case "ILIKE":
	sVal, okS := goVal.(string)
	patVal, okP := condVal.(string)
	if okS && okP {
		return matchLike(lowerText(sVal), lowerText(patVal)), nil
	}
	return false, nil
```
`lowerText` lowers ASCII and Unicode letters. Use the lowering helper the repo or `webtyp.com/fmt` already has (`grep -rn "ToLower" *.go ../fmt/*.go | head`); never import stdlib `strings` in a file the browser compiles (`indexdb` is a browser backend). If none exists, write the loop over `[]rune` using `unicode`-free range tables only if `fmt` has none, and say so in the PR. Also add `"ILIKE"` wherever the file lists the operators it understands (`grep -n '"LIKE"' *.go`), including any index-selection code that must NOT use an index for a pattern.

## Stage 2 — test
`TestILike_IgnoresCase` in `tests/ilike_test.go` (wasm-dual pattern of the repo's other tests: read `tests/` first): rows `Ana`, `ana maría`, `Bob`; `ILike("name","%AN%")` → first two. Row in `docs/TESTS.md`. If the repo runs the `storage/conformance` suite, it now passes the ILike clause.

## Delivery check
`gotest` passes on every destination (stdlib and wasm); a tree that does not compile is a failed delivery.

| Stage | Files |
|---|---|
| 1 | `execute.go` |
| 2 | `tests/ilike_test.go`, `docs/TESTS.md` |
