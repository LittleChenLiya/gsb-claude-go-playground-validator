package validator

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	. "github.com/go-playground/assert/v2"
)

// ---------------------------------------------------------------------------
// Acceptance: min rule renders different text in two languages
// ---------------------------------------------------------------------------

type messageMinStruct struct {
	Age  int    `validate:"min=18"`
	Name string `validate:"min=3"`
}

func TestMessageRegistryMinTwoLanguages(t *testing.T) {
	v := New()

	err := v.RegisterMessageTemplate("min", "en", nil, "{field} must be at least {param}")
	Equal(t, err, nil)
	err = v.RegisterMessageTemplate("min", "zh", nil, "{field} 必须至少为 {param}")
	Equal(t, err, nil)

	s := messageMinStruct{Age: 10, Name: "ab"}
	errs := v.Struct(s)
	NotEqual(t, errs, nil)

	verrs, ok := errs.(ValidationErrors)
	Equal(t, ok, true)
	Equal(t, len(verrs), 2)

	en, err := v.RenderMessage(verrs[0], "en")
	Equal(t, err, nil)
	zh, err := v.RenderMessage(verrs[0], "zh")
	Equal(t, err, nil)

	NotEqual(t, en.Text, zh.Text)
	MatchRegex(t, en.Text, "must be at least")
	MatchRegex(t, zh.Text, "必须至少为")

	// the second field renders in the same two languages with its own name
	en2, err := v.RenderMessage(verrs[1], "en")
	Equal(t, err, nil)
	zh2, err := v.RenderMessage(verrs[1], "zh")
	Equal(t, err, nil)
	NotEqual(t, en2.Text, zh2.Text)
	MatchRegex(t, en2.Text, "Name")
	MatchRegex(t, zh2.Text, "Name")
}

// ---------------------------------------------------------------------------
// Acceptance: field-type dedicated template takes priority over the tag default
// ---------------------------------------------------------------------------

func TestMessageRegistryFieldTypePriority(t *testing.T) {
	v := New()

	err := v.RegisterMessageDefault("min", "{field} failed the min check of {param}")
	Equal(t, err, nil)
	err = v.RegisterMessageTemplate("min", "en", reflect.TypeOf(0), "int field {field} must be >= {param}")
	Equal(t, err, nil)
	err = v.RegisterMessageTemplate("min", "en", reflect.TypeOf(""), "string field {field} must be >= {param}")
	Equal(t, err, nil)

	s := messageMinStruct{Age: 10, Name: "ab"}
	errs := v.Struct(s)
	verrs := errs.(ValidationErrors)

	// Age is an int -> the int-dedicated template wins, not the default
	age, err := v.RenderMessage(verrs[0], "en")
	Equal(t, err, nil)
	Equal(t, age.Text, "int field Age must be >= 18")
	Equal(t, age.Source, MessageSource{Tag: "min", Lang: "en", Type: reflect.TypeOf(0)})
	Equal(t, age.Fallback, false)

	// Name is a string -> the string-dedicated template wins
	name, err := v.RenderMessage(verrs[1], "en")
	Equal(t, err, nil)
	Equal(t, name.Text, "string field Name must be >= 3")
	Equal(t, name.Source, MessageSource{Tag: "min", Lang: "en", Type: reflect.TypeOf("")})
	Equal(t, name.Fallback, false)

	// a language without a dedicated template falls back to the tag default
	ageZh, err := v.RenderMessage(verrs[0], "zh")
	Equal(t, err, nil)
	Equal(t, ageZh.Text, "Age failed the min check of 18")
	Equal(t, ageZh.Source, MessageSource{Tag: "min", Lang: "", Type: nil})
	Equal(t, ageZh.Fallback, true)
}

// ---------------------------------------------------------------------------
// Acceptance: unknown placeholder reports position at compile time
// ---------------------------------------------------------------------------

func TestMessageRegistryUnknownPlaceholderPosition(t *testing.T) {
	r := NewMessageRegistry()

	err := r.Register("min", "en", nil, "line one\n{valeu} is not a valid placeholder")
	NotEqual(t, err, nil)

	var te *TemplateError
	Equal(t, errors.As(err, &te), true)
	Equal(t, te.Line, 2)
	Equal(t, te.Col, 1)
	MatchRegex(t, err.Error(), "line 2, column 1")
	MatchRegex(t, err.Error(), "valeu")

	// a placeholder mid-line reports its exact column
	err = r.Register("min", "en", nil, "prefix {bad} suffix")
	NotEqual(t, err, nil)
	Equal(t, errors.As(err, &te), true)
	Equal(t, te.Line, 1)
	Equal(t, te.Col, 8)
}

func TestMessageRegistryMalformedPlaceholders(t *testing.T) {
	r := NewMessageRegistry()

	err := r.Register("min", "en", nil, "unclosed {field")
	NotEqual(t, err, nil)
	var te *TemplateError
	Equal(t, errors.As(err, &te), true)
	Equal(t, te.Line, 1)
	Equal(t, te.Col, 10)

	err = r.Register("min", "en", nil, "stray } brace")
	NotEqual(t, err, nil)
	Equal(t, errors.As(err, &te), true)
	Equal(t, te.Col, 7)
}

// ---------------------------------------------------------------------------
// Acceptance: conflicting registrations are rejected at compile time
// ---------------------------------------------------------------------------

func TestMessageRegistryConflict(t *testing.T) {
	r := NewMessageRegistry()

	err := r.Register("min", "en", nil, "first")
	Equal(t, err, nil)

	err = r.Register("min", "en", nil, "second")
	NotEqual(t, err, nil)
	var te *TemplateError
	Equal(t, errors.As(err, &te), true)
	MatchRegex(t, err.Error(), "conflicting")

	// different language / type is not a conflict
	err = r.Register("min", "zh", nil, "second")
	Equal(t, err, nil)
	err = r.Register("min", "en", reflect.TypeOf(0), "second")
	Equal(t, err, nil)
}

// ---------------------------------------------------------------------------
// Acceptance: fallback source is reported together with the message
// ---------------------------------------------------------------------------

func TestMessageRegistryFallbackSource(t *testing.T) {
	r := NewMessageRegistry()

	// only a per-tag default exists -> rendering falls back and reports it
	err := r.RegisterDefault("min", "{field} must be >= {param}")
	Equal(t, err, nil)

	fe := &fieldError{
		tag:      "min",
		ns:       "Age",
		fieldLen: 3,
		param:    "18",
		kind:     reflect.Int,
		typ:      reflect.TypeOf(0),
	}

	rm, err := r.Render(fe, "en")
	Equal(t, err, nil)
	Equal(t, rm.Text, "Age must be >= 18")
	Equal(t, rm.Fallback, true)
	Equal(t, rm.Source, MessageSource{Tag: "min", Lang: "", Type: nil})

	// a global fallback is reported with the fallback tag
	err = r.SetFallback("{field} failed validation")
	Equal(t, err, nil)

	fe2 := &fieldError{
		tag:      "email",
		ns:       "Email",
		fieldLen: 5,
		typ:      reflect.TypeOf(""),
	}
	rm2, err := r.Render(fe2, "en")
	Equal(t, err, nil)
	Equal(t, rm2.Text, "Email failed validation")
	Equal(t, rm2.Fallback, true)
	Equal(t, rm2.Source.Tag, messageFallbackTag)
}

// ---------------------------------------------------------------------------
// Acceptance: nested indexes ({index0}, {index1}, {index})
// ---------------------------------------------------------------------------

type messageTestItem struct {
	Value int `validate:"min=10"`
}

type messageTestGroup struct {
	Items []messageTestItem `validate:"dive"`
}

type messageTestOuter struct {
	Groups []messageTestGroup `validate:"dive"`
}

func TestMessageRegistryNestedIndex(t *testing.T) {
	v := New()

	err := v.RegisterMessageTemplate("min", "en", nil,
		"item at {index0}.{index1} (innermost {index}) must be >= {param}")
	Equal(t, err, nil)

	s := messageTestOuter{
		Groups: []messageTestGroup{
			{Items: []messageTestItem{{Value: 1}, {Value: 2}}},
			{Items: []messageTestItem{{Value: 3}}},
		},
	}
	errs := v.Struct(s)
	NotEqual(t, errs, nil)
	verrs := errs.(ValidationErrors)
	Equal(t, len(verrs), 3)

	// Groups[0].Items[0].Value -> indexes 0,0
	rm, err := v.RenderMessage(verrs[0], "en")
	Equal(t, err, nil)
	Equal(t, rm.Text, "item at 0.0 (innermost 0) must be >= 10")

	// Groups[0].Items[1].Value -> indexes 0,1
	rm, err = v.RenderMessage(verrs[1], "en")
	Equal(t, err, nil)
	Equal(t, rm.Text, "item at 0.1 (innermost 1) must be >= 10")

	// Groups[1].Items[0].Value -> indexes 1,0
	rm, err = v.RenderMessage(verrs[2], "en")
	Equal(t, err, nil)
	Equal(t, rm.Text, "item at 1.0 (innermost 0) must be >= 10")

	// the namespace really carries the nested indexes
	MatchRegex(t, verrs[1].Namespace(), `Groups\[0\]\.Items\[1\]\.Value`)
}

// ---------------------------------------------------------------------------
// Acceptance: a missing template is a render error, never a validation success
// and the FieldError structure is preserved
// ---------------------------------------------------------------------------

func TestMessageRegistryMissingTemplateIsError(t *testing.T) {
	v := New()

	// no templates registered at all
	fe := &fieldError{
		tag:      "required",
		ns:       "Name",
		fieldLen: 4,
		param:    "",
		kind:     reflect.String,
		typ:      reflect.TypeOf(""),
	}

	rm, err := v.RenderMessage(fe, "en")
	NotEqual(t, err, nil)
	Equal(t, rm.Text, "")

	var mte *MissingTemplateError
	Equal(t, errors.As(err, &mte), true)
	Equal(t, mte.Tag, "required")
	Equal(t, mte.Lang, "en")

	// the FieldError is untouched: its original error still renders
	Equal(t, fe.Error(), "Key: 'Name' Error:Field validation for 'Name' failed on the 'required' tag")
	Equal(t, fe.Tag(), "required")
	Equal(t, fe.Field(), "Name")

	// and validation itself still fails - missing templates never swallow it
	type reqStruct struct {
		Name string `validate:"required"`
	}
	errs := v.Struct(reqStruct{})
	NotEqual(t, errs, nil)
	verrs := errs.(ValidationErrors)
	Equal(t, len(verrs), 1)

	rendered, err := verrs.RenderMessages(v, "en")
	NotEqual(t, err, nil)
	Equal(t, len(rendered), 0)
	var mtes *MissingTemplatesError
	Equal(t, errors.As(err, &mtes), true)
	Equal(t, len(mtes.Missing), 1)
}

func TestMessageRegistryRenderMessagesPartial(t *testing.T) {
	v := New()

	err := v.RegisterMessageTemplate("min", "en", nil, "{field} >= {param}")
	Equal(t, err, nil)
	// "required" has no template -> it must be reported as missing

	type mixedStruct struct {
		Name string `validate:"required"`
		Age  int    `validate:"min=18"`
	}
	errs := v.Struct(mixedStruct{})
	verrs := errs.(ValidationErrors)
	Equal(t, len(verrs), 2)

	rendered, err := verrs.RenderMessages(v, "en")
	NotEqual(t, err, nil)
	Equal(t, len(rendered), 1)
	Equal(t, rendered[0].Text, "Age >= 18")

	var mtes *MissingTemplatesError
	Equal(t, errors.As(err, &mtes), true)
	Equal(t, len(mtes.Missing), 1)
	Equal(t, mtes.Missing[0].Tag, "required")
}

// ---------------------------------------------------------------------------
// Template spec: raw values are never inserted, only registered placeholders
// ---------------------------------------------------------------------------

func TestMessageRegistryNoRawValues(t *testing.T) {
	r := NewMessageRegistry()

	// {value} is not a registered placeholder and must be rejected
	err := r.Register("min", "en", nil, "{field} = {value}")
	NotEqual(t, err, nil)
	var te *TemplateError
	Equal(t, errors.As(err, &te), true)
	MatchRegex(t, err.Error(), "value")

	// the whitelist renders only the allowed pieces
	err = r.Register("min", "en", nil, "{field}/{param}/{index}/{index0}")
	Equal(t, err, nil)

	fe := &fieldError{
		tag:      "min",
		ns:       "Items[2].Value",
		fieldLen: 5,
		param:    "5",
		typ:      reflect.TypeOf(0),
	}
	rm, err := r.Render(fe, "en")
	Equal(t, err, nil)
	Equal(t, rm.Text, "Value/5/2/2")
	Equal(t, strings.Contains(rm.Text, "Value/5/2/2"), true)
}
