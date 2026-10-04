package validator

import (
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"sync"
)

// Message templates: an explicit, compiled error-message rendering plan.
//
// A MessageRegistry stores human-readable message templates keyed by the
// triple (tag, language, field type). Templates are compiled (parsed) at
// registration time, so unknown placeholders and conflicting registrations
// are reported before any validation runs - this is the "compile time"
// check of the rendering plan.
//
// # Template syntax
//
// A template is a plain string containing placeholders in curly braces:
//
//	"{field} must be at least {param} (got value at index {index})"
//
// Only the following placeholders are allowed; anything else is a
// compile-time TemplateError reporting the placeholder's position:
//
//	{field}   - the field's name (FieldError.Field()), never its value
//	{param}   - the rule's parameter (FieldError.Param())
//	{index}   - the innermost dive index of the field (e.g. the "1" in "Items[1]")
//	{indexN}  - the dive index at nesting level N (0-based, outermost first):
//	            "{index0}" is the outermost index, "{index1}" the next one, ...
//
// Raw field values are deliberately NOT available as placeholders: a
// template can only render the registered field name, the rule parameter
// and dive indexes. This keeps messages free of unchecked user data.
//
// # Matching and priority
//
// Render(fe, lang) matches a template for the failed tag using the first
// key that exists in this order:
//
//  1. tag + lang + field type   (most specific, field-type dedicated)
//  2. tag + lang + wildcard type
//  3. tag + wildcard lang + field type
//  4. tag + wildcard lang + wildcard type  (per-tag default)
//  5. global fallback template registered with SetFallback
//
// Every fallback is reported in RenderedMessage.Source together with the
// Fallback flag, so callers can tell exactly which template produced the
// message.
//
// A missing template is a render error (MissingTemplateError), never an
// empty string and never a validation success: the FieldError itself is
// left untouched and the surrounding validation still fails.

// messageKey is the lookup key of a compiled template.
type messageKey struct {
	tag  string
	lang string       // empty means language wildcard
	typ  reflect.Type // nil means type wildcard
}

// MessageSource identifies the template a rendered message was produced
// from. It is reported back together with every rendered message so that
// fallback rendering is observable.
type MessageSource struct {
	// Tag is the tag the template was registered for. The global fallback
	// template is reported with Tag == messageFallbackTag.
	Tag string
	// Lang is the language the template was registered for; empty means
	// the language wildcard matched.
	Lang string
	// Type is the field type the template was registered for; nil means
	// the type wildcard matched.
	Type reflect.Type
}

// messageFallbackTag is the Tag reported for the global fallback template.
const messageFallbackTag = "*"

// RenderedMessage is the result of rendering a FieldError against a
// MessageRegistry.
type RenderedMessage struct {
	// Text is the rendered message.
	Text string
	// Source identifies the template that produced Text.
	Source MessageSource
	// Fallback is true when a wildcard or the global fallback was used
	// instead of an exact (tag, lang, type) match.
	Fallback bool
}

// TemplateError is a compile-time error in a message template: either an
// unknown/malformed placeholder or a conflicting registration.
type TemplateError struct {
	// Tag, Lang and Type identify the registration the template was
	// attempted for.
	Tag  string
	Lang string
	Type reflect.Type
	// Raw is the unparsed template string.
	Raw string
	// Line and Col are the 1-based position of the offending placeholder
	// within Raw. They are 0 when the error is not position related (e.g.
	// a conflicting registration).
	Line int
	Col  int
	// Msg describes the problem.
	Msg string
}

func (e *TemplateError) Error() string {
	var b strings.Builder
	b.WriteString("validator: message template error")
	if e.Tag != "" {
		fmt.Fprintf(&b, " for tag %q", e.Tag)
	}
	if e.Lang != "" {
		fmt.Fprintf(&b, " lang %q", e.Lang)
	}
	if e.Type != nil {
		fmt.Fprintf(&b, " type %s", e.Type.String())
	}
	if e.Line > 0 {
		fmt.Fprintf(&b, " at line %d, column %d", e.Line, e.Col)
	}
	b.WriteString(": ")
	b.WriteString(e.Msg)
	return b.String()
}

// MissingTemplateError is a render error: no template matched the failed
// tag, language and field type. Render returns it instead of producing an
// empty message; the validation error itself is preserved.
type MissingTemplateError struct {
	Tag  string
	Lang string
	Type reflect.Type
}

func (e *MissingTemplateError) Error() string {
	return fmt.Sprintf("validator: no message template registered for tag %q (lang %q, type %s)",
		e.Tag, e.Lang, messageTypeString(e.Type))
}

// messageSegment is either a piece of literal text or a compiled
// placeholder. Exactly one of text and placeholder is set.
type messageSegment struct {
	text        string
	placeholder string
}

// MessageTemplate is a compiled message template.
type MessageTemplate struct {
	tag      string
	lang     string
	typ      reflect.Type
	raw      string
	segments []messageSegment
}

// render executes the compiled template against fe.
func (t *MessageTemplate) render(fe FieldError) string {
	var b strings.Builder

	indexes := extractDiveIndexes(fe.Namespace())

	for _, seg := range t.segments {
		if seg.placeholder == "" {
			b.WriteString(seg.text)
			continue
		}

		switch {
		case seg.placeholder == "field":
			b.WriteString(fe.Field())
		case seg.placeholder == "param":
			b.WriteString(fe.Param())
		case seg.placeholder == "index":
			if n, ok := lastDiveIndex(indexes); ok {
				b.WriteString(strconv.Itoa(n))
			}
		case strings.HasPrefix(seg.placeholder, "index"):
			if n, err := strconv.Atoi(seg.placeholder[len("index"):]); err == nil {
				if n >= 0 && n < len(indexes) {
					b.WriteString(strconv.Itoa(indexes[n]))
				}
			}
		}
	}

	return b.String()
}

// MessageRegistry is an explicit, compiled registry of error message
// templates. See the package documentation for the full template
// specification.
//
// MessageRegistry is safe for concurrent use; templates are meant to be
// registered during setup, before validation runs.
type MessageRegistry struct {
	mu        sync.RWMutex
	templates map[messageKey]*MessageTemplate
	fallback  *MessageTemplate
}

// NewMessageRegistry returns an empty MessageRegistry.
func NewMessageRegistry() *MessageRegistry {
	return &MessageRegistry{templates: make(map[messageKey]*MessageTemplate)}
}

// Register compiles template and registers it for the given tag, language
// and field type. lang == "" is the language wildcard, typ == nil is the
// type wildcard.
//
// Compile-time checks: unknown or malformed placeholders are reported
// with their position, and registering a template for a key that already
// has a template is reported as a conflicting priority.
func (r *MessageRegistry) Register(tag, lang string, typ reflect.Type, template string) error {
	if tag == "" {
		return errors.New("validator: message template tag cannot be empty")
	}

	segments, err := compileTemplate(tag, lang, typ, template)
	if err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	key := messageKey{tag: tag, lang: lang, typ: typ}
	if _, ok := r.templates[key]; ok {
		return &TemplateError{
			Tag:  tag,
			Lang: lang,
			Type: typ,
			Raw:  template,
			Msg:  "conflicting template: a template with the same tag, language and field type is already registered",
		}
	}

	r.templates[key] = &MessageTemplate{
		tag:      tag,
		lang:     lang,
		typ:      typ,
		raw:      template,
		segments: segments,
	}
	return nil
}

// RegisterDefault registers the per-tag default template (language and
// type wildcard). It is used when no more specific template matches.
func (r *MessageRegistry) RegisterDefault(tag, template string) error {
	return r.Register(tag, "", nil, template)
}

// SetFallback registers the global fallback template, used for any tag
// that has no matching template at all. Pass an empty template to remove
// the fallback.
func (r *MessageRegistry) SetFallback(template string) error {
	if template == "" {
		r.mu.Lock()
		r.fallback = nil
		r.mu.Unlock()
		return nil
	}

	segments, err := compileTemplate(messageFallbackTag, "", nil, template)
	if err != nil {
		return err
	}

	r.mu.Lock()
	r.fallback = &MessageTemplate{
		tag:      messageFallbackTag,
		raw:      template,
		segments: segments,
	}
	r.mu.Unlock()
	return nil
}

// Render looks the failed tag up following the priority order
// (tag+lang+type, tag+lang, tag+type, tag default, global fallback) and
// renders the first match.
//
// It returns a MissingTemplateError when no template matches, so a missing
// template is never reported as a successful (empty) render. The
// FieldError is never modified.
func (r *MessageRegistry) Render(fe FieldError, lang string) (RenderedMessage, error) {
	if fe == nil {
		return RenderedMessage{}, errors.New("validator: cannot render message for a nil FieldError")
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	tmpl, source, fallback := r.resolve(fe.Tag(), lang, fe.Type())
	if tmpl == nil {
		// Aliases register against the alias name; fall back to the
		// actual tag, mirroring FieldError.Translate.
		tmpl, source, fallback = r.resolve(fe.ActualTag(), lang, fe.Type())
	}
	if tmpl == nil {
		return RenderedMessage{}, &MissingTemplateError{
			Tag:  fe.Tag(),
			Lang: lang,
			Type: fe.Type(),
		}
	}

	return RenderedMessage{
		Text:     tmpl.render(fe),
		Source:   source,
		Fallback: fallback,
	}, nil
}

// resolve finds the template for (tag, lang, typ) following the documented
// priority order. The caller holds at least a read lock.
func (r *MessageRegistry) resolve(tag, lang string, typ reflect.Type) (*MessageTemplate, MessageSource, bool) {
	// 1. tag + lang + field type (exact)
	if tmpl, ok := r.templates[messageKey{tag, lang, typ}]; ok {
		return tmpl, MessageSource{Tag: tag, Lang: lang, Type: typ}, false
	}
	// 2. tag + lang + wildcard type
	if tmpl, ok := r.templates[messageKey{tag, lang, nil}]; ok {
		return tmpl, MessageSource{Tag: tag, Lang: lang, Type: nil}, true
	}
	// 3. tag + wildcard lang + field type
	if tmpl, ok := r.templates[messageKey{tag, "", typ}]; ok {
		return tmpl, MessageSource{Tag: tag, Lang: "", Type: typ}, true
	}
	// 4. tag default (wildcard lang + wildcard type)
	if tmpl, ok := r.templates[messageKey{tag, "", nil}]; ok {
		return tmpl, MessageSource{Tag: tag, Lang: "", Type: nil}, true
	}
	// 5. global fallback
	if r.fallback != nil {
		return r.fallback, MessageSource{Tag: messageFallbackTag, Lang: "", Type: nil}, true
	}
	return nil, MessageSource{}, false
}

// compileTemplate parses and validates a template, reporting unknown or
// malformed placeholders with their line/column position.
func compileTemplate(tag, lang string, typ reflect.Type, raw string) ([]messageSegment, error) {
	var segments []messageSegment
	var literal strings.Builder

	line, col := 1, 1
	i := 0
	for i < len(raw) {
		if raw[i] == '{' {
			rel := strings.IndexByte(raw[i+1:], '}')
			if rel == -1 {
				return nil, &TemplateError{
					Tag:  tag,
					Lang: lang,
					Type: typ,
					Raw:  raw,
					Line: line,
					Col:  col,
					Msg:  "unclosed placeholder: missing '}'",
				}
			}

			end := i + 1 + rel
			name := raw[i+1 : end]

			if !isAllowedPlaceholder(name) {
				return nil, &TemplateError{
					Tag:  tag,
					Lang: lang,
					Type: typ,
					Raw:  raw,
					Line: line,
					Col:  col,
					Msg:  fmt.Sprintf("unknown placeholder %q: allowed placeholders are {field}, {param}, {index} and {indexN}", name),
				}
			}

			if literal.Len() > 0 {
				segments = append(segments, messageSegment{text: literal.String()})
				literal.Reset()
			}
			segments = append(segments, messageSegment{placeholder: name})

			advance(&line, &col, raw[i:end+1])
			i = end + 1
			continue
		}

		if raw[i] == '}' {
			return nil, &TemplateError{
				Tag:  tag,
				Lang: lang,
				Type: typ,
				Raw:  raw,
				Line: line,
				Col:  col,
				Msg:  "stray '}': placeholders must be opened with '{'",
			}
		}

		literal.WriteByte(raw[i])
		if raw[i] == '\n' {
			line++
			col = 1
		} else {
			col++
		}
		i++
	}

	if literal.Len() > 0 {
		segments = append(segments, messageSegment{text: literal.String()})
	}

	return segments, nil
}

// isAllowedPlaceholder reports whether name is one of the registered
// placeholders: field, param, index or indexN.
func isAllowedPlaceholder(name string) bool {
	if name == "field" || name == "param" || name == "index" {
		return true
	}
	if strings.HasPrefix(name, "index") && len(name) > len("index") {
		for _, r := range name[len("index"):] {
			if r < '0' || r > '9' {
				return false
			}
		}
		return true
	}
	return false
}

// advance moves line/col forward over the consumed bytes.
func advance(line, col *int, consumed string) {
	for _, r := range consumed {
		if r == '\n' {
			*line++
			*col = 1
		} else {
			*col++
		}
	}
}

// extractDiveIndexes returns the numeric dive indexes encoded in a
// namespace, outermost first. Map keys and other non-numeric segments are
// ignored.
func extractDiveIndexes(ns string) []int {
	var indexes []int
	for i := 0; i < len(ns); i++ {
		if ns[i] != '[' {
			continue
		}
		j := i + 1
		for j < len(ns) && ns[j] >= '0' && ns[j] <= '9' {
			j++
		}
		if j > i+1 && j < len(ns) && ns[j] == ']' {
			n, _ := strconv.Atoi(ns[i+1 : j])
			indexes = append(indexes, n)
		}
	}
	return indexes
}

// lastDiveIndex returns the innermost (last) dive index.
func lastDiveIndex(indexes []int) (int, bool) {
	if len(indexes) == 0 {
		return 0, false
	}
	return indexes[len(indexes)-1], true
}

func messageTypeString(typ reflect.Type) string {
	if typ == nil {
		return "<any>"
	}
	return typ.String()
}

// RegisterMessageTemplate compiles and registers a message template on the
// validator's internal MessageRegistry. lang == "" is the language
// wildcard, typ == nil is the type wildcard. See MessageRegistry.Register.
//
// NOTE: this function is not thread-safe; register templates during setup
// before validation runs.
func (v *Validate) RegisterMessageTemplate(tag, lang string, typ reflect.Type, template string) error {
	return v.MessageRegistry().Register(tag, lang, typ, template)
}

// RegisterMessageDefault registers the per-tag default message template on
// the validator's internal MessageRegistry. See MessageRegistry.RegisterDefault.
//
// NOTE: this function is not thread-safe; register templates during setup
// before validation runs.
func (v *Validate) RegisterMessageDefault(tag, template string) error {
	return v.MessageRegistry().RegisterDefault(tag, template)
}

// SetMessageFallback registers the global fallback message template on the
// validator's internal MessageRegistry. See MessageRegistry.SetFallback.
//
// NOTE: this function is not thread-safe; register templates during setup
// before validation runs.
func (v *Validate) SetMessageFallback(template string) error {
	return v.MessageRegistry().SetFallback(template)
}

// RenderMessage renders fe against the validator's internal MessageRegistry
// for the given language. A missing template is returned as an error; the
// FieldError is never modified.
func (v *Validate) RenderMessage(fe FieldError, lang string) (RenderedMessage, error) {
	return v.MessageRegistry().Render(fe, lang)
}

// MessageRegistry returns the validator's internal MessageRegistry,
// allowing direct use of the full rendering API.
func (v *Validate) MessageRegistry() *MessageRegistry {
	if v.messages == nil {
		v.messages = NewMessageRegistry()
	}
	return v.messages
}

// RenderMessages renders every FieldError against the validator's
// internal MessageRegistry for the given language. It returns the
// rendered messages in order; if one or more templates are missing it
// returns the rendered messages together with a MissingTemplatesError, so
// a missing template is never reported as a successful render and never
// swallows the validation failure.
func (ve ValidationErrors) RenderMessages(v *Validate, lang string) ([]RenderedMessage, error) {
	rendered := make([]RenderedMessage, 0, len(ve))
	var missing []MissingTemplateError

	for _, fe := range ve {
		rm, err := v.RenderMessage(fe, lang)
		if err != nil {
			var mte *MissingTemplateError
			if errors.As(err, &mte) {
				missing = append(missing, *mte)
				continue
			}
			return rendered, err
		}
		rendered = append(rendered, rm)
	}

	if len(missing) > 0 {
		return rendered, &MissingTemplatesError{Missing: missing}
	}
	return rendered, nil
}

// MissingTemplatesError collects the tags for which no message template
// matched while rendering a ValidationErrors.
type MissingTemplatesError struct {
	Missing []MissingTemplateError
}

func (e *MissingTemplatesError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "validator: %d field error(s) have no registered message template:", len(e.Missing))
	for i := range e.Missing {
		b.WriteString("\n  - ")
		b.WriteString(e.Missing[i].Error())
	}
	return b.String()
}
