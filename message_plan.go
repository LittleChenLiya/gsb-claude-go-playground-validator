package validator

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"
)

// This file implements an explicit error message rendering plan
// ("MessagePlan"). It is a deterministic message rendering layer on top
// of FieldError: templates are matched by validation tag, field type
// (reflect.Kind) and language (locale), with an explicit default-message
// fallback chain.
//
// It does NOT change the existing translation registration interface
// (RegisterTranslation / TranslationFunc / RegisterTranslationsFunc);
// both mechanisms can coexist.
//
// Template specification (see MESSAGE_PLAN.md for the full spec):
//
//   - Placeholders use the form {name} or {name[N]}.
//   - Allowed placeholders (registered fields, rule params and indexes
//     only; raw field values are NEVER exposed):
//       {field}           FieldError.Field()
//       {structField}     FieldError.StructField()
//       {namespace}       FieldError.Namespace()
//       {structNamespace} FieldError.StructNamespace()
//       {tag}             FieldError.Tag()
//       {actualTag}       FieldError.ActualTag()
//       {locale}          the locale requested at render time
//       {param}           FieldError.Param() (whole rule parameter)
//       {param[N]}        Nth space-separated rule parameter (e.g. oneof)
//       {index}           all namespace indexes joined by "." (nested, e.g. "0.1")
//       {index[N]}        Nth namespace index (nested slices/maps)
//   - "{{" renders a literal "{".
//   - Unknown placeholders and unterminated "{" are compile-time errors
//     that report the byte position within the template text.
//
// Matching and priority (deterministic, higher score wins):
//
//	field-specific  (Field set)  : 8
//	kind-specific   (Kind set)   : 4
//	locale-specific (Locale set) : 2
//	tag-specific    (Tag set)    : 1
//
// Two templates with the identical (Locale, Tag, Field, Kind) key are a
// priority conflict and are rejected at compile time.
//
// Fallback chain at render time (the source is always reported):
//
//	best matching template -> locale default -> plan default ->
//	FieldError.Error() (SourceErrorFallback, ErrMissingTemplate set)
//
// A missing template is NEVER treated as validation success: RenderedError
// always carries the original FieldError and an explicit Source/Err.

// RenderSource identifies which layer of a MessagePlan produced a message.
type RenderSource int

const (
	// SourceFieldTemplate indicates a field-specific template was used.
	SourceFieldTemplate RenderSource = iota
	// SourceKindTemplate indicates a field-type (reflect.Kind) template was used.
	SourceKindTemplate
	// SourceTagTemplate indicates a tag (+optional locale) template was used.
	SourceTagTemplate
	// SourceLocaleDefault indicates the locale-specific default message was used.
	SourceLocaleDefault
	// SourcePlanDefault indicates the plan-wide default message was used.
	SourcePlanDefault
	// SourceErrorFallback indicates no template matched at all; the message
	// is FieldError.Error() and ErrMissingTemplate is reported. This is an
	// explicit fallback, never a silent validation success.
	SourceErrorFallback
)

// String returns a human readable name of the RenderSource.
func (s RenderSource) String() string {
	switch s {
	case SourceFieldTemplate:
		return "field-template"
	case SourceKindTemplate:
		return "kind-template"
	case SourceTagTemplate:
		return "tag-template"
	case SourceLocaleDefault:
		return "locale-default"
	case SourcePlanDefault:
		return "plan-default"
	case SourceErrorFallback:
		return "error-fallback"
	default:
		return "unknown"
	}
}

// MessageTemplate is the specification of a single message template.
// An empty Locale/Tag/Field or reflect.Invalid Kind matches any value
// of that dimension, but at least one of Tag, Field or Kind must be set
// (use plan defaults for catch-all messages).
type MessageTemplate struct {
	Locale string       // language, e.g. "en", "zh"; "" matches any locale
	Tag    string       // validation tag, e.g. "min"; "" matches any tag
	Field  string       // field name (Field() or StructField()); "" matches any field
	Kind   reflect.Kind // field type; reflect.Invalid matches any kind
	Text   string       // template text with {placeholders}
}

// TemplateError is a compile-time template error; Pos is the byte
// position within Text at which the problem was found (-1 if N/A).
type TemplateError struct {
	Template MessageTemplate
	Pos      int
	Msg      string
}

// Error returns the error message including the position within the template.
func (e *TemplateError) Error() string {
	return fmt.Sprintf("validator: message template (locale=%q tag=%q field=%q): %s at position %d in %q",
		e.Template.Locale, e.Template.Tag, e.Template.Field, e.Msg, e.Pos, e.Template.Text)
}

// TemplateConflictError reports two templates with conflicting priority:
// they share the identical (Locale, Tag, Field, Kind) key.
type TemplateConflictError struct {
	A, B MessageTemplate
}

// Error returns the conflict description.
func (e *TemplateConflictError) Error() string {
	return fmt.Sprintf("validator: message template priority conflict between %q and %q (same locale/tag/field/kind key)",
		e.A.Text, e.B.Text)
}

// ErrMissingTemplate is reported on RenderedError.Err when no template and
// no default matched; the message falls back to FieldError.Error() and the
// original FieldError is preserved. It is never treated as success.
var ErrMissingTemplate = fmt.Errorf("validator: no message template matched; explicit fallback to FieldError.Error()")

// specificity weights for template matching
const (
	weightField  = 8
	weightKind   = 4
	weightLocale = 2
	weightTag    = 1
)

// placeholder is a compiled {name} or {name[N]} reference.
type placeholder struct {
	name     string // field, structField, namespace, structNamespace, tag, actualTag, locale, param, index
	hasIndex bool   // true for {param[N]} / {index[N]}
	index    int    // N
}

// tplPart is one compiled piece of a template: either a literal or a placeholder.
type tplPart struct {
	lit string
	ph  *placeholder
}

// compiledTemplate is a parsed MessageTemplate with its match key.
type compiledTemplate struct {
	spec  MessageTemplate
	parts []tplPart
	score int
}

// MessagePlan is a compiled, deterministic error message rendering plan.
type MessagePlan struct {
	templates []*compiledTemplate
	defaults  map[string]*compiledTemplate // keyed by locale; "" is the plan-wide default
}

// knownPlaceholders is the registry of allowed placeholder names.
// NOTE: there is intentionally no "value" placeholder; raw field values
// are never inserted into messages.
var knownPlaceholders = map[string]bool{
	"field":           true,
	"structField":     true,
	"namespace":       true,
	"structNamespace": true,
	"tag":             true,
	"actualTag":       true,
	"locale":          true,
	"param":           true,
	"index":           true,
}

// CompileMessagePlan compiles defaults and templates into a MessagePlan.
//
// defaults maps a locale ("" for the plan-wide default) to a default
// template text; defaults are compiled with the same placeholder rules.
//
// Compile-time checks:
//   - unknown placeholders are rejected with their byte position;
//   - unterminated "{" is rejected with its byte position;
//   - templates with no matcher (Tag, Field and Kind all empty) are rejected;
//   - templates with identical (Locale, Tag, Field, Kind) keys conflict.
func CompileMessagePlan(defaults map[string]string, templates ...MessageTemplate) (*MessagePlan, error) {
	p := &MessagePlan{defaults: make(map[string]*compiledTemplate, len(defaults))}

	for locale, text := range defaults {
		ct, err := compileTemplate(MessageTemplate{Locale: locale, Text: text}, true)
		if err != nil {
			return nil, err
		}
		if _, ok := p.defaults[locale]; ok {
			return nil, &TemplateConflictError{
				A: MessageTemplate{Locale: locale, Text: text},
				B: MessageTemplate{Locale: locale, Text: text},
			}
		}
		p.defaults[locale] = ct
	}

	seen := make(map[[4]string]MessageTemplate, len(templates))
	for _, t := range templates {
		if t.Tag == "" && t.Field == "" && t.Kind == reflect.Invalid {
			return nil, &TemplateError{Template: t, Pos: -1, Msg: "template must match at least one of tag, field or kind"}
		}
		key := [4]string{t.Locale, t.Tag, t.Field, strconv.Itoa(int(t.Kind))}
		if prev, ok := seen[key]; ok {
			return nil, &TemplateConflictError{A: prev, B: t}
		}
		seen[key] = t

		ct, err := compileTemplate(t, false)
		if err != nil {
			return nil, err
		}
		p.templates = append(p.templates, ct)
	}
	return p, nil
}

// compileTemplate parses the template text into parts and computes the score.
func compileTemplate(t MessageTemplate, isDefault bool) (*compiledTemplate, error) {
	parts, err := parseTemplateText(t)
	if err != nil {
		return nil, err
	}
	ct := &compiledTemplate{spec: t, parts: parts}
	if !isDefault {
		if t.Field != "" {
			ct.score += weightField
		}
		if t.Kind != reflect.Invalid {
			ct.score += weightKind
		}
		if t.Locale != "" {
			ct.score += weightLocale
		}
		if t.Tag != "" {
			ct.score += weightTag
		}
	}
	return ct, nil
}

// parseTemplateText parses {placeholders} out of the template text.
func parseTemplateText(t MessageTemplate) ([]tplPart, error) {
	text := t.Text
	var parts []tplPart
	var lit strings.Builder

	flush := func() {
		if lit.Len() > 0 {
			parts = append(parts, tplPart{lit: lit.String()})
			lit.Reset()
		}
	}

	for i := 0; i < len(text); i++ {
		if text[i] != '{' {
			lit.WriteByte(text[i])
			continue
		}
		if i+1 < len(text) && text[i+1] == '{' { // escaped "{{"
			lit.WriteByte('{')
			i++
			continue
		}
		end := strings.IndexByte(text[i:], '}')
		if end < 0 {
			return nil, &TemplateError{Template: t, Pos: i, Msg: "unterminated '{'"}
		}
		end += i
		ph, err := parsePlaceholder(t, text[i+1:end], i)
		if err != nil {
			return nil, err
		}
		flush()
		parts = append(parts, tplPart{ph: ph})
		i = end
	}
	flush()
	return parts, nil
}

// parsePlaceholder parses the inside of "{...}"; pos is the byte position
// of the opening '{' within the template text.
func parsePlaceholder(t MessageTemplate, body string, pos int) (*placeholder, error) {
	name, idxStr := body, ""
	hasIdx := false
	if open := strings.IndexByte(body, '['); open >= 0 {
		if !strings.HasSuffix(body, "]") {
			return nil, &TemplateError{Template: t, Pos: pos, Msg: fmt.Sprintf("malformed placeholder index in {%s}", body)}
		}
		name, idxStr, hasIdx = body[:open], body[open+1:len(body)-1], true
	}
	if !knownPlaceholders[name] {
		return nil, &TemplateError{Template: t, Pos: pos, Msg: fmt.Sprintf("unknown placeholder {%s}", body)}
	}
	ph := &placeholder{name: name}
	if hasIdx {
		if name != "param" && name != "index" {
			return nil, &TemplateError{Template: t, Pos: pos, Msg: fmt.Sprintf("placeholder {%s} does not accept an index", body)}
		}
		n, err := strconv.Atoi(idxStr)
		if err != nil || n < 0 {
			return nil, &TemplateError{Template: t, Pos: pos, Msg: fmt.Sprintf("invalid placeholder index in {%s}", body)}
		}
		ph.hasIndex, ph.index = true, n
	}
	return ph, nil
}

// matches reports whether the template matches the error/locale.
func (ct *compiledTemplate) matches(fe FieldError, locale string) bool {
	s := ct.spec
	if s.Locale != "" && s.Locale != locale {
		return false
	}
	if s.Tag != "" && s.Tag != fe.Tag() && s.Tag != fe.ActualTag() {
		return false
	}
	if s.Field != "" && s.Field != fe.Field() && s.Field != fe.StructField() {
		return false
	}
	if s.Kind != reflect.Invalid && s.Kind != fe.Kind() {
		return false
	}
	return true
}

// source returns the RenderSource for a matched template.
func (ct *compiledTemplate) source() RenderSource {
	s := ct.spec
	switch {
	case s.Field != "":
		return SourceFieldTemplate
	case s.Kind != reflect.Invalid:
		return SourceKindTemplate
	default:
		return SourceTagTemplate
	}
}

// RenderedError is the result of rendering one FieldError through a plan.
// It always preserves the original FieldError structure and reports the
// fallback source that produced the message.
type RenderedError struct {
	Message    string       // rendered message (FieldError.Error() on fallback/render error)
	Locale     string       // locale the message was rendered for
	Source     RenderSource // which plan layer produced the message (fallback source included)
	Err        error        // render error or ErrMissingTemplate; nil on clean render
	FieldError FieldError   // the original error, always preserved
}

// Render renders a single FieldError for the given locale.
//
// Resolution order: best matching template (highest specificity score),
// locale default, plan-wide default, FieldError.Error(). A missing template
// is reported explicitly (Source == SourceErrorFallback, Err ==
// ErrMissingTemplate) and never treated as validation success.
func (p *MessagePlan) Render(fe FieldError, locale string) RenderedError {
	re := RenderedError{Locale: locale, FieldError: fe}

	var best *compiledTemplate
	for _, ct := range p.templates {
		if ct.matches(fe, locale) && (best == nil || ct.score > best.score) {
			best = ct
		}
	}

	switch {
	case best != nil:
		re.Source = best.source()
		re.Message, re.Err = best.render(fe, locale)
		if re.Err != nil {
			// render error: preserve the FieldError structure, fall back
			// to the original error message, keep the failing source.
			re.Message = fe.Error()
		}
	case p.defaults[locale] != nil:
		re.Source = SourceLocaleDefault
		re.Message, re.Err = p.defaults[locale].render(fe, locale)
		if re.Err != nil {
			re.Message = fe.Error()
		}
	case p.defaults[""] != nil:
		re.Source = SourcePlanDefault
		re.Message, re.Err = p.defaults[""].render(fe, locale)
		if re.Err != nil {
			re.Message = fe.Error()
		}
	default:
		re.Source = SourceErrorFallback
		re.Message = fe.Error()
		re.Err = ErrMissingTemplate
	}
	return re
}

// RenderAll renders every error in a ValidationErrors for the given locale.
func (p *MessagePlan) RenderAll(errs ValidationErrors, locale string) []RenderedError {
	out := make([]RenderedError, len(errs))
	for i, fe := range errs {
		out[i] = p.Render(fe, locale)
	}
	return out
}

// render executes a compiled template against a FieldError.
func (ct *compiledTemplate) render(fe FieldError, locale string) (string, error) {
	var b strings.Builder
	for _, part := range ct.parts {
		if part.ph == nil {
			b.WriteString(part.lit)
			continue
		}
		s, err := resolvePlaceholder(part.ph, fe, locale)
		if err != nil {
			return "", err
		}
		b.WriteString(s)
	}
	return b.String(), nil
}

// resolvePlaceholder resolves one placeholder against the FieldError.
// Only registered fields, rule params and indexes are exposed; never
// the raw field value.
func resolvePlaceholder(ph *placeholder, fe FieldError, locale string) (string, error) {
	switch ph.name {
	case "field":
		return fe.Field(), nil
	case "structField":
		return fe.StructField(), nil
	case "namespace":
		return fe.Namespace(), nil
	case "structNamespace":
		return fe.StructNamespace(), nil
	case "tag":
		return fe.Tag(), nil
	case "actualTag":
		return fe.ActualTag(), nil
	case "locale":
		return locale, nil
	case "param":
		if !ph.hasIndex {
			return fe.Param(), nil
		}
		params := strings.Fields(fe.Param())
		if ph.index >= len(params) {
			return "", fmt.Errorf("validator: rule param index %d out of range (%d params) for field %q",
				ph.index, len(params), fe.Field())
		}
		return params[ph.index], nil
	case "index":
		indexes := namespaceIndexes(fe.Namespace())
		if !ph.hasIndex {
			return strings.Join(indexes, "."), nil
		}
		if ph.index >= len(indexes) {
			return "", fmt.Errorf("validator: namespace index %d out of range (%d indexes) for field %q",
				ph.index, len(indexes), fe.Field())
		}
		return indexes[ph.index], nil
	}
	return "", fmt.Errorf("validator: unhandled placeholder {%s}", ph.name)
}

// namespaceIndexes extracts the bracketed indexes from a namespace,
// supporting nesting, e.g. "Matrix[0][1]" -> ["0", "1"],
// "Order.Items[2].Name" -> ["2"].
func namespaceIndexes(ns string) []string {
	var out []string
	for i := 0; i < len(ns); i++ {
		if ns[i] != '[' {
			continue
		}
		end := strings.IndexByte(ns[i:], ']')
		if end < 0 {
			break
		}
		out = append(out, ns[i+1:i+end])
		i += end
	}
	return out
}
