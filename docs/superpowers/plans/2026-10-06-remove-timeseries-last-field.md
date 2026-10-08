# Remove the cached `TimeSeries.last` field (future work)

Status: not scheduled, low priority. Nothing here is implemented.

## Problem

`timeseries.TimeSeries` (`timeseries/timeseries.go:22`) keeps `last float32`, a copy of `data[len(data)-1]`, so that `Last()` does not index the slice. It was added in commit `3f91c89` ("timeseries: optimize `Last` function", 2023-11-13, no measurement in the message).

- It saves a length check and one load per `Last()` call: nanoseconds. `IsEmpty()` is just `ts == nil` (line 389), so without the field `Last()` needs one more check (`len(ts.data) == 0`).
- It costs 4 bytes per series and a rule every writer must follow: set `ts.last` whenever the last point can change. A new method that forgets it makes `Last()` return a stale value with no compile error or test failure unless a test happens to look. The field is set in about 11 places (lines 74, 94, 96, 125, 128, 160, 206, 253, 299, 353, 456).
- `Last()` has ~186 callers outside tests (auditors, views, `TailIsEmpty`), so it is cheap to keep the API and change only the internals.

No profile in the repo says `Last()` matters; per request, building the `World` (allocating a `TimeSeries` for every series of every query) costs far more.

## Goal

`Last()` and `TailIsEmpty()` read the last element of `data`; the `last` field and all writes to it are gone. No change in behaviour, no change in the public API, no serialized format change (`last` is unexported and not encoded: `EncodeMsgpack` / `DecodeMsgpack` / JSON use `data`).

## Steps

1. **Measure first (optional, 10 minutes).** Add `BenchmarkLast` to `timeseries/timeseries_test.go` (a series of 240 points, `Last()` in a loop) and run it before and after. If the difference is below noise, say so in the commit message. If it is a real loss in a hot path (check `auditor/` with `go test -bench` on a real `World` if one is available), stop and keep the field.
2. **Change `Last()`:**

```go
func (ts *TimeSeries) Last() float32 {
	if ts.IsEmpty() || len(ts.data) == 0 {
		return NaN
	}
	return ts.data[len(ts.data)-1]
}
```

3. **Change `TailIsEmpty()`** (line 400): replace `ts.last` with `ts.data[len(ts.data)-1]`; it already reads `l := len(ts.data)`, so use that and handle `l == 0` (return true).
4. **Delete the field and every assignment** to `ts.last` (the lines listed above). `go build ./timeseries` lists any left.
5. **Tests.** `go test ./timeseries ./model ./auditor ./constructor ./api/... ./watchers`. Add a test that a series made with `New` and then `Set` on the last index, `MapInPlace`, `FillAny`/`FillAvg`/`FillSum`/`FillMax` and `UnmarshalJSON` of `null` and `[]` returns the expected `Last()` (NaN for `null`/empty), since these are the paths that used to maintain `last`.
6. **Verify.** `make test-e2e` (names and internals only: a failure means a wrong edit).

## Risks

- A path that relied on `last` being `NaN` for an empty `data` (`UnmarshalJSON` of `[]`, `NewWithData` with an empty slice): the `len(ts.data) == 0` check in `Last()` and `TailIsEmpty()` covers it; the test in step 5 pins it.
- `Aggregate` and other types in the package that wrap a `TimeSeries` might read `last` directly: `grep -n "\.last\b" timeseries/*.go` found `timeseries.go` only when this plan was written (2026-10-06); re-check before starting.
- A diff that touches the core package for no behaviour gain: do it together with other `timeseries` changes (for example the rename in `2026-10-06-rename-constructor-cache.md` does not touch it, but any later change to `timeseries` can carry this), or skip it.
