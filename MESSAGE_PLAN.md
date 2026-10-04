# Message Plan — Explicit Error Message Rendering

`message_plan.go` implements an explicit, compile-checked error message
rendering plan. Templates are matched by **tag**, **field type**
(`reflect.Kind`) and **language** (locale), with the default message as an
explicit, always-reported fallback. It is a rendering layer on top of
`FieldError`; the existing translation registration interface
(`RegisterTranslation`, `TranslationFunc`, `RegisterTranslationsFunc`) is
unchanged and both mechanisms can coexist.

## Building a plan

```go
plan, err := validator.CompileMessagePlan(
    map[string]string{ // defaults: "" is the plan-wide default
        "en": "{field} is invalid",
        "":  "validation failed",
    },
    validator.MessageTemplate{Locale: "en", Tag: "min", Kind: reflect.Int,
        Text: "{field} must be at least {param}"},
    validator.MessageTemplate{Locale: "zh", Tag: "min", Kind: reflect.Int,
        Text: "{field} 不能小于 {param}"},
    validator.MessageTemplate{Locale: "en", Tag: "min", Field: "Age",
        Text: "{field} must be >= {param} (field-specific)"},
)
```

Rendering:

```go
errs := validate.Struct(input) // validator.ValidationErrors
for _, re := range plan.RenderAll(errs.(validator.ValidationErrors), "en") {
    // re.Message, re.Source, re.Err, re.FieldError
}
```

## Template matching dimensions

| MessageTemplate field | Matches                                    | Empty value  |
|-----------------------|--------------------------------------------|--------------|
| `Locale`              | requested render locale                    | any locale   |
| `Tag`                 | `FieldError.Tag()` / `ActualTag()`         | any tag      |
| `Field`               | `FieldError.Field()` / `StructField()`     | any field    |
| `Kind`                | `FieldError.Kind()`                        | `reflect.Invalid` = any |

At least one of `Tag`, `Field`, `Kind` must be set (use defaults for
catch-all messages).

## Priority

Specificity score, higher wins: field = 8, kind = 4, locale = 2, tag = 1.
A field-specific template therefore always beats tag/kind templates.
Two templates with the identical `(Locale, Tag, Field, Kind)` key are a
priority conflict and fail compilation with `*TemplateConflictError`.

## Placeholders

Only **registered fields, rule params and indexes** may be referenced —
raw field values are never inserted (there is intentionally no `{value}`).

| Placeholder         | Resolves to                                            |
|---------------------|--------------------------------------------------------|
| `{field}`           | `FieldError.Field()`                                   |
| `{structField}`     | `FieldError.StructField()`                             |
| `{namespace}`       | `FieldError.Namespace()`                               |
| `{structNamespace}` | `FieldError.StructNamespace()`                         |
| `{tag}`             | `FieldError.Tag()`                                     |
| `{actualTag}`       | `FieldError.ActualTag()`                               |
| `{locale}`          | locale requested at render time                        |
| `{param}`           | whole rule parameter, `FieldError.Param()`             |
| `{param[N]}`        | Nth space-separated rule parameter (e.g. `oneof`)      |
| `{index}`           | all namespace indexes joined by `.` (nested: `0.1`)    |
| `{index[N]}`        | Nth namespace index, e.g. `Matrix[0][1]` → `{index[1]}` = `1` |

`{{` renders a literal `{`.

## Compile-time checks

`CompileMessagePlan` rejects, with the byte **position** in the template
text (`*TemplateError.Pos`):

- unknown placeholders (e.g. `{value}`),
- unterminated `{`, malformed or non-numeric indexes,
- indexed use of placeholders that take no index,
- templates with no matcher,
- priority conflicts (`*TemplateConflictError`).

## Fallback chain (source always reported)

1. best matching template → `SourceFieldTemplate` / `SourceKindTemplate` / `SourceTagTemplate`
2. locale default → `SourceLocaleDefault`
3. plan-wide default → `SourcePlanDefault`
4. `FieldError.Error()` → `SourceErrorFallback` with `Err == ErrMissingTemplate`

Every `RenderedError` carries the original `FieldError` structure. A
missing template is an explicit fallback with `ErrMissingTemplate`, never
a silent validation success. Render-time failures (e.g. `{param[3]}` out
of range) set `RenderedError.Err`, keep the matched source, and fall back
to the original `FieldError.Error()` message.

## Tests

```bash
go test -run 'TestMessagePlan' -v ./
```
