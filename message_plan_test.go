package validator

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func mustPlan(t *testing.T, defaults map[string]string, templates ...MessageTemplate) *MessagePlan {
	t.Helper()
	p, err := CompileMessagePlan(defaults, templates...)
	if err != nil {
		t.Fatalf("CompileMessagePlan failed: %v", err)
	}
	return p
}

func firstFieldError(t *testing.T, err error) FieldError {
	t.Helper()
	if err == nil {
		t.Fatal("expected validation error, got nil")
	}
	ve, ok := err.(ValidationErrors)
	if !ok {
		t.Fatalf("expected ValidationErrors, got %T", err)
	}
	if len(ve) == 0 {
		t.Fatal("expected at least one FieldError")
	}
	return ve[0]
}

// Acceptance: the min rule renders different texts in two languages.
func TestMessagePlanMinTwoLanguages(t *testing.T) {
	type User struct {
		Age int `validate:"min=18"`
	}
	plan := mustPlan(t, nil,
		MessageTemplate{Locale: "en", Tag: "min", Kind: reflect.Int, Text: "{field} must be at least {param}"},
		MessageTemplate{Locale: "zh", Tag: "min", Kind: reflect.Int, Text: "{field} 不能小于 {param}"},
	)

	validate := New()
	fe := firstFieldError(t, validate.Struct(User{Age: 3}))

	en := plan.Render(fe, "en")
	zh := plan.Render(fe, "zh")

	if en.Err != nil || zh.Err != nil {
		t.Fatalf("unexpected render errors: en=%v zh=%v", en.Err, zh.Err)
	}
	if en.Message == zh.Message {
		t.Fatalf("expected different texts per language, both were %q", en.Message)
	}
	if en.Message != "Age must be at least 18" {
		t.Fatalf("unexpected en message: %q", en.Message)
	}
	if zh.Message != "Age 不能小于 18" {
		t.Fatalf("unexpected zh message: %q", zh.Message)
	}
	if en.Source != SourceKindTemplate || zh.Source != SourceKindTemplate {
		t.Fatalf("unexpected sources: %v / %v", en.Source, zh.Source)
	}
}

// Acceptance: a field-specific template wins over tag/kind templates.
func TestMessagePlanFieldSpecificPriority(t *testing.T) {
	type User struct {
		Age int `validate:"min=18"`
	}
	plan := mustPlan(t, nil,
		MessageTemplate{Locale: "en", Tag: "min", Text: "tag-level: {field} >= {param}"},
		MessageTemplate{Locale: "en", Tag: "min", Kind: reflect.Int, Text: "kind-level: {field} >= {param}"},
		MessageTemplate{Locale: "en", Tag: "min", Field: "Age", Text: "field-level: {field} must be >= {param}"},
	)

	validate := New()
	fe := firstFieldError(t, validate.Struct(User{Age: 3}))
	re := plan.Render(fe, "en")

	if re.Source != SourceFieldTemplate {
		t.Fatalf("expected field template source, got %v", re.Source)
	}
	if re.Message != "field-level: Age must be >= 18" {
		t.Fatalf("unexpected message: %q", re.Message)
	}
}

// Acceptance: unknown placeholders are compile-time errors with position.
func TestMessagePlanUnknownPlaceholderPosition(t *testing.T) {
	text := "{field} failed: {value} is invalid"
	_, err := CompileMessagePlan(nil,
		MessageTemplate{Locale: "en", Tag: "min", Text: text},
	)
	if err == nil {
		t.Fatal("expected compile error for unknown placeholder")
	}
	te, ok := err.(*TemplateError)
	if !ok {
		t.Fatalf("expected *TemplateError, got %T (%v)", err, err)
	}
	wantPos := strings.Index(text, "{value}")
	if te.Pos != wantPos {
		t.Fatalf("expected position %d, got %d", wantPos, te.Pos)
	}
	if !strings.Contains(te.Error(), "unknown placeholder") {
		t.Fatalf("error should mention unknown placeholder: %v", te)
	}

	// unterminated brace also reports its position
	_, err = CompileMessagePlan(nil,
		MessageTemplate{Locale: "en", Tag: "min", Text: "{field} is {param"},
	)
	te2, ok := err.(*TemplateError)
	if !ok || te2.Pos != strings.Index("{field} is {param", "{param") {
		t.Fatalf("expected unterminated brace at position 11, got %v", err)
	}
}

// Acceptance: conflicting priorities are rejected at compile time.
func TestMessagePlanConflictingPriority(t *testing.T) {
	_, err := CompileMessagePlan(nil,
		MessageTemplate{Locale: "en", Tag: "min", Text: "one"},
		MessageTemplate{Locale: "en", Tag: "min", Text: "two"},
	)
	if err == nil {
		t.Fatal("expected conflict error")
	}
	if _, ok := err.(*TemplateConflictError); !ok {
		t.Fatalf("expected *TemplateConflictError, got %T (%v)", err, err)
	}

	// different specificity keys do not conflict
	_ = mustPlan(t, nil,
		MessageTemplate{Locale: "en", Tag: "min", Text: "tag"},
		MessageTemplate{Locale: "en", Tag: "min", Field: "Age", Text: "field"},
	)
}

// Acceptance: nested indexes are supported via {index} / {index[N]}.
func TestMessagePlanNestedIndexes(t *testing.T) {
	type Grid struct {
		Matrix [][]string `validate:"dive,dive,min=2"`
	}
	plan := mustPlan(t, nil,
		MessageTemplate{Locale: "en", Tag: "min", Text: "{namespace}: cell [{index[0]}][{index[1]}] (path {index}) too short, min {param}"},
	)

	validate := New()
	grid := Grid{Matrix: [][]string{{"ok", "x"}}}
	fe := firstFieldError(t, validate.Struct(grid))

	if fe.Namespace() != "Grid.Matrix[0][1]" {
		t.Fatalf("unexpected namespace: %q", fe.Namespace())
	}
	re := plan.Render(fe, "en")
	if re.Err != nil {
		t.Fatalf("unexpected render error: %v", re.Err)
	}
	want := "Grid.Matrix[0][1]: cell [0][1] (path 0.1) too short, min 2"
	if re.Message != want {
		t.Fatalf("expected %q, got %q", want, re.Message)
	}
}

// Acceptance: fallback is reported together with its source, and a missing
// template is never treated as validation success.
func TestMessagePlanFallbackSources(t *testing.T) {
	type User struct {
		Age  int    `validate:"min=18"`
		Name string `validate:"required"`
	}

	// locale default only
	plan := mustPlan(t, map[string]string{
		"en": "default-en: {field} invalid on {tag}",
		"":   "default-any: {field} invalid",
	})
	validate := New()
	err := validate.Struct(User{Age: 3})
	ve := err.(ValidationErrors)
	re := plan.Render(ve[0], "en")
	if re.Source != SourceLocaleDefault {
		t.Fatalf("expected locale default source, got %v", re.Source)
	}
	if re.Message != "default-en: Age invalid on min" {
		t.Fatalf("unexpected message: %q", re.Message)
	}

	// plan-wide default for an unregistered locale
	re = plan.Render(ve[0], "fr")
	if re.Source != SourcePlanDefault {
		t.Fatalf("expected plan default source, got %v", re.Source)
	}
	if re.Message != "default-any: Age invalid" {
		t.Fatalf("unexpected message: %q", re.Message)
	}

	// no template and no default at all: explicit error fallback,
	// FieldError structure preserved, NOT a validation success.
	empty := mustPlan(t, nil, MessageTemplate{Locale: "de", Tag: "min", Text: "nur deutsch"})
	re = empty.Render(ve[0], "en")
	if re.Source != SourceErrorFallback {
		t.Fatalf("expected error fallback source, got %v", re.Source)
	}
	if !errors.Is(re.Err, ErrMissingTemplate) {
		t.Fatalf("expected ErrMissingTemplate, got %v", re.Err)
	}
	if re.Message != ve[0].Error() {
		t.Fatalf("expected original error message, got %q", re.Message)
	}
	if re.FieldError == nil || re.FieldError.Tag() != "min" || re.FieldError.Field() != "Age" {
		t.Fatalf("FieldError structure not preserved: %+v", re.FieldError)
	}
}

// Render errors (e.g. param index out of range) keep the FieldError
// structure and fall back to the original error message.
func TestMessagePlanRenderErrorPreservesFieldError(t *testing.T) {
	type User struct {
		Age int `validate:"min=18"`
	}
	plan := mustPlan(t, nil,
		MessageTemplate{Locale: "en", Tag: "min", Text: "{field} needs {param[1]}"},
	)
	validate := New()
	fe := firstFieldError(t, validate.Struct(User{Age: 3}))
	re := plan.Render(fe, "en")

	if re.Err == nil {
		t.Fatal("expected render error for out-of-range param index")
	}
	if re.Message != fe.Error() {
		t.Fatalf("expected fallback to original error, got %q", re.Message)
	}
	if re.FieldError != fe {
		t.Fatal("FieldError structure not preserved on render error")
	}
}

// Rule params: {param[N]} selects the Nth space-separated parameter.
func TestMessagePlanRuleParamIndex(t *testing.T) {
	type Config struct {
		Color string `validate:"oneof=red green blue"`
	}
	plan := mustPlan(t, nil,
		MessageTemplate{Locale: "en", Tag: "oneof", Text: "{field} must be one of [{param}], e.g. {param[1]}"},
	)
	validate := New()
	fe := firstFieldError(t, validate.Struct(Config{Color: "purple"}))
	re := plan.Render(fe, "en")
	if re.Err != nil {
		t.Fatalf("unexpected render error: %v", re.Err)
	}
	if re.Message != "Color must be one of [red green blue], e.g. green" {
		t.Fatalf("unexpected message: %q", re.Message)
	}
}

// RenderAll renders every error and preserves each FieldError.
func TestMessagePlanRenderAll(t *testing.T) {
	type User struct {
		Age  int    `validate:"min=18"`
		Name string `validate:"required"`
	}
	plan := mustPlan(t, nil,
		MessageTemplate{Locale: "en", Tag: "min", Text: "{field} too small"},
		MessageTemplate{Locale: "en", Tag: "required", Text: "{field} is required"},
	)
	validate := New()
	err := validate.Struct(User{})
	out := plan.RenderAll(err.(ValidationErrors), "en")
	if len(out) != 2 {
		t.Fatalf("expected 2 rendered errors, got %d", len(out))
	}
	for _, re := range out {
		if re.Err != nil || re.FieldError == nil {
			t.Fatalf("unexpected render state: %+v", re)
		}
	}
}
