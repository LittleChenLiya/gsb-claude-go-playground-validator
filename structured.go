package validator

import (
	"reflect"
	"strconv"
	"strings"
)

// pathSegment records a single step of the path traversed during validation.
//
// Segments are recorded while the validator walks the data, so the structured
// error representation never has to guess structure by parsing error strings.
type pathSegment struct {
	structName string       // Go struct field name, empty for index/key segments
	name       string       // external (tag name func) name, may be empty
	index      int64        // slice/array index when isIndex is set
	isIndex    bool         // segment is a slice/array index
	key        string       // string form of a map key when isKey is set
	keyKind    reflect.Kind // reflect.Kind of the map key
	isKey      bool         // segment is a map key
}

// StructuredError is a machine-readable representation of a single field
// validation failure, built from the path recorded during validation.
//
// It is intended for API responses so clients can locate the offending
// request field directly:
//
//   - Namespace is the Go field namespace, eg. "Request.Items[1].Name".
//   - FieldPath is the external field path relative to the top-level struct,
//     using TagNameFunc names (falling back to Go field names), eg.
//     "items[1].name". Non-string map keys render as typed segments, eg.
//     "counts[int(5)]".
//   - Pointer is the RFC 6901 JSON Pointer for the failing value, eg.
//     "/items/1/name". It is empty when no faithful pointer exists: when a
//     non-string map key is involved (no pointer is fabricated) or when the
//     error is unresolved.
//   - Tag and ActualTag preserve the original failed constraint, including
//     aliases and 'or' combinations, exactly as FieldError reports them.
//   - Unresolved is true when the path could not be proven from the
//     validated structure (eg. custom StructLevel paths); Namespace keeps
//     the original reported namespace in that case.
//   - Value is empty by default so sensitive data (eg. passwords) is not
//     leaked into API responses; opt in with WithValues.
type StructuredError struct {
	Namespace  string      `json:"namespace"`
	FieldPath  string      `json:"field_path"`
	Pointer    string      `json:"pointer,omitempty"`
	Tag        string      `json:"tag"`
	ActualTag  string      `json:"actual_tag"`
	Param      string      `json:"param,omitempty"`
	Unresolved bool        `json:"unresolved,omitempty"`
	Value      interface{} `json:"value,omitempty"`
}

// StructuredOption customizes the conversion performed by
// ValidationErrors.Structured.
type StructuredOption func(*structuredConfig)

type structuredConfig struct {
	withValues bool
}

// WithValues includes the failing field values in the returned
// StructuredErrors. Values are excluded by default to avoid leaking
// sensitive data such as passwords into API responses.
func WithValues() StructuredOption {
	return func(c *structuredConfig) {
		c.withValues = true
	}
}

// Structured converts the ValidationErrors into their structured
// representation, suitable for returning to API clients.
//
// The original ValidationErrors behaviour is untouched; this is a pure
// conversion built on the paths recorded during validation.
func (ve ValidationErrors) Structured(opts ...StructuredOption) []StructuredError {
	var cfg structuredConfig
	for _, opt := range opts {
		opt(&cfg)
	}

	se := make([]StructuredError, 0, len(ve))
	for i := 0; i < len(ve); i++ {
		fe, ok := ve[i].(*fieldError)
		if !ok {
			// foreign FieldError implementation, no recorded path available
			se = append(se, StructuredError{
				Namespace:  ve[i].StructNamespace(),
				FieldPath:  ve[i].Namespace(),
				Tag:        ve[i].Tag(),
				ActualTag:  ve[i].ActualTag(),
				Param:      ve[i].Param(),
				Unresolved: true,
			})
			continue
		}
		se = append(se, fe.structured(cfg.withValues))
	}
	return se
}

// structured builds the StructuredError for a single fieldError from the
// path recorded while traversing the data.
func (fe *fieldError) structured(withValue bool) StructuredError {
	se := StructuredError{
		Namespace:  fe.structNs,
		Tag:        fe.tag,
		ActualTag:  fe.actualTag,
		Param:      fe.param,
		Unresolved: fe.unresolved,
	}
	if withValue {
		se.Value = fe.value
	}

	if fe.unresolved {
		// the path cannot be proven from the validated structure; keep the
		// original reported namespace and do not fabricate a JSON Pointer.
		se.FieldPath = fe.ns
		return se
	}

	var pathSB, ptrSB strings.Builder
	ptrOK := true

	for i := range fe.path {
		seg := &fe.path[i]
		switch {
		case seg.isIndex:
			idx := strconv.FormatInt(seg.index, 10)
			pathSB.WriteByte('[')
			pathSB.WriteString(idx)
			pathSB.WriteByte(']')
			ptrSB.WriteByte('/')
			ptrSB.WriteString(idx)
		case seg.isKey:
			if seg.keyKind == reflect.String {
				pathSB.WriteByte('[')
				pathSB.WriteString(seg.key)
				pathSB.WriteByte(']')
				ptrSB.WriteByte('/')
				ptrSB.WriteString(escapeJSONPointer(seg.key))
			} else {
				// non-string map keys have no JSON Pointer representation;
				// record a typed path segment and leave the pointer empty.
				pathSB.WriteByte('[')
				pathSB.WriteString(seg.keyKind.String())
				pathSB.WriteByte('(')
				pathSB.WriteString(seg.key)
				pathSB.WriteString(")]")
				ptrOK = false
			}
		default:
			name := seg.name
			if len(name) == 0 {
				// no external name mapped, fall back to the Go field name
				name = seg.structName
			}
			if pathSB.Len() > 0 {
				pathSB.WriteByte('.')
			}
			pathSB.WriteString(name)
			ptrSB.WriteByte('/')
			ptrSB.WriteString(escapeJSONPointer(name))
		}
	}

	se.FieldPath = pathSB.String()
	if ptrOK {
		se.Pointer = ptrSB.String()
	}
	return se
}

var jsonPointerEscaper = strings.NewReplacer("~", "~0", "/", "~1")

// escapeJSONPointer escapes a single reference token as defined by
// RFC 6901: '~' becomes '~0' and '/' becomes '~1'.
func escapeJSONPointer(token string) string {
	if !strings.ContainsAny(token, "~/") {
		return token
	}
	return jsonPointerEscaper.Replace(token)
}
