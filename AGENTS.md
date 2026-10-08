# Agent guidelines

## Naming

- Prefer slightly longer, descriptive variable names that a human can read without context.
- Avoid abbreviations such as `ts`, `cfg`, `req`, `idx`. Write `timeSeries`, `config`, `request`, `index`.
- Constructors are always named `New<StructName>` (for example `NewTimeSeries`), never a bare `New`.

## Helpers and wrappers

- Do not add a wrapper function (or a local closure) that only forwards its arguments to another function. A shorter call site is not a good enough reason. Call the underlying function directly.
- Add a helper only when it does real work: validation, defaults, error handling, combining several calls, or hiding a detail that callers should not know.
