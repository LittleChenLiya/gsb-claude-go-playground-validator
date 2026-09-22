package validator

import (
	"strconv"
	"strings"
)

// SegmentKind identifies what a single PathSegment of a StructuredError represents.
type SegmentKind uint8

const (
	// SegmentRoot is the root struct type name, it is part of the Go namespace
	// and external field path but is never part of the JSON Pointer.
	SegmentRoot SegmentKind = iota
	// SegmentField is a struct field. Field holds the Go field name and Name
	// the external name resolved via RegisterTagNameFunc (empty means they are equal).
	SegmentField
	// SegmentIndex is a slice/array index recorded while traversing 'dive'.
	SegmentIndex
	// SegmentMapKey is a string map key recorded while traversing 'dive'.
	SegmentMapKey
	// SegmentTypedMapKey is a non-string map key. Key holds its string form and
	// KeyType its Go type. No JSON Pointer is generated for paths containing one.
	SegmentTypedMapKey
	// SegmentUnresolved is a path fragment reported through StructLevel custom
	// validation that could not be proven to map to a real field. The original
	// text is preserved and the whole error is flagged Unresolved.
	SegmentUnresolved
)

// PathSegment is a single recorded step of the path to a failed field.
type PathSegment struct {
	Kind    SegmentKind `json:"kind"`
	Field   string      `json:"field,omitempty"`    // Go field/struct name, or raw text for SegmentUnresolved
	Name    string      `json:"name,omitempty"`     // external name (RegisterTagNameFunc), empty when equal to Field
	Index   *int        `json:"index,omitempty"`    // set for SegmentIndex
	Key     string      `json:"key,omitempty"`      // map key in string form
	KeyType string      `json:"key_type,omitempty"` // map key Go type, set for SegmentTypedMapKey only
}

// StructuredError is a structured, API-friendly representation of a single
// field validation failure. All paths are generated from the path recorded
// during the actual validation run, never parsed back from error strings.
type StructuredError struct {
	// Namespace is the Go field namespace, eg. "User.Items[1].Name".
	Namespace string `json:"namespace"`
	// FieldPath is the external field path using the names registered via
	// RegisterTagNameFunc, falling back to the Go field name when unmapped,
	// eg. "User.items[1].name".
	FieldPath string `json:"field_path"`
	// Pointer is the RFC 6901 JSON Pointer to the failed field, eg.
	// "/items/1/name". String map keys have '~' and '/' escaped as '~0'/'~1'.
	// It is empty when the path contains a non-string map key or an
	// unresolved StructLevel fragment, in which case no pointer is fabricated.
	Pointer string `json:"pointer"`
	// Tag is the failed validation tag; for aliases this is the alias name.
	Tag string `json:"tag"`
	// ActualTag is the actual failed validation tag, preserving the original
	// failed constraint for aliases and 'or' combinations.
	ActualTag string `json:"actual_tag"`
	// Param is the failed tag's parameter, if any.
	Param string `json:"param,omitempty"`
	// Path is the recorded path segments, including typed map keys.
	Path []PathSegment `json:"path,omitempty"`
	// Unresolved is true when the path contains StructLevel custom fragments
	// that could not be proven; Namespace/FieldPath still carry the original text.
	Unresolved bool `json:"unresolved,omitempty"`
	// Value is the failed field's value. It is only populated when
	// WithValues is passed to Structured; it is omitted by default so
	// sensitive values such as passwords are not returned to API clients.
	Value interface{} `json:"value,omitempty"`
}

// internal aliases used while recording paths during validation.
const (
	segRoot       = SegmentRoot
	segField      = SegmentField
	segIndex      = SegmentIndex
	segKeyString  = SegmentMapKey
	segKeyTyped   = SegmentTypedMapKey
	segUnresolved = SegmentUnresolved
)

// internal compact path segment recorded during validation.
type pathSegment struct {
	kind    SegmentKind
	name    string // Go field/struct name, map key string form, or raw unresolved text
	altName string // external name (segField) or raw external text (segUnresolved)
	index   int    // segIndex
	keyType string // segKeyTyped
}

func copySegments(segs []pathSegment) []pathSegment {
	if len(segs) == 0 {
		return nil
	}
	out := make([]pathSegment, len(segs))
	copy(out, segs)
	return out
}

// isSimplePathName reports whether s is a plain field name, ie. it contains
// no nested path syntax such as '.' or '[...]'.
func isSimplePathName(s string) bool {
	return len(s) > 0 && !strings.ContainsAny(s, ".[]")
}

// pointerEscaper escapes JSON Pointer (RFC 6901) reference tokens.
var pointerEscaper = strings.NewReplacer("~", "~0", "/", "~1")

// structuredConfig holds optional settings for Structured.
type structuredConfig struct {
	withValues bool
}

// StructuredOption customizes the output of ValidationErrors.Structured.
type StructuredOption func(*structuredConfig)

// WithValues includes the failed fields' values in the returned
// StructuredErrors. Values are omitted by default so sensitive data
// (eg. passwords) is not accidentally returned to API clients.
func WithValues() StructuredOption {
	return func(c *structuredConfig) { c.withValues = true }
}

// Structured converts the ValidationErrors into a slice of StructuredError,
// generating the Go namespace, external field path, JSON Pointer, failed tag,
// actual tag and parameter from the path recorded during validation.
//
// Field values are omitted by default; pass WithValues to include them.
// The original ValidationErrors/FieldError interface is unaffected.
func (ve ValidationErrors) Structured(opts ...StructuredOption) []StructuredError {
	var cfg structuredConfig
	for _, opt := range opts {
		opt(&cfg)
	}

	out := make([]StructuredError, len(ve))
	for i, e := range ve {
		se := StructuredError{
			Tag:       e.Tag(),
			ActualTag: e.ActualTag(),
			Param:     e.Param(),
		}

		if fe, ok := e.(*fieldError); ok {
			se.Namespace, se.FieldPath, se.Pointer, se.Path, se.Unresolved = buildStructuredPath(fe.path)
		} else {
			// foreign FieldError implementation, no recorded path available.
			se.Namespace = e.StructNamespace()
			se.FieldPath = e.Namespace()
			se.Unresolved = true
		}

		if cfg.withValues {
			se.Value = e.Value()
		}

		out[i] = se
	}

	return out
}

// buildStructuredPath renders the recorded path segments into the Go
// namespace, external field path, JSON Pointer and exported segments.
func buildStructuredPath(path []pathSegment) (goNS, extPath, pointer string, segs []PathSegment, unresolved bool) {
	var ns, ext, ptr []byte
	pointerOK := true

	for _, s := range path {
		switch s.kind {
		case segRoot:
			ns = appendNameToken(ns, s.name)
			ext = appendNameToken(ext, s.name)
			segs = append(segs, PathSegment{Kind: SegmentRoot, Field: s.name})

		case segField:
			ns = appendNameToken(ns, s.name)
			if len(s.altName) > 0 {
				ext = appendNameToken(ext, s.altName)
			}
			if pointerOK {
				name := s.altName
				if len(name) == 0 {
					name = s.name
				}
				ptr = append(ptr, '/')
				ptr = append(ptr, pointerEscaper.Replace(name)...)
			}
			segs = append(segs, PathSegment{Kind: SegmentField, Field: s.name, Name: s.altName})

		case segIndex:
			idx := strconv.Itoa(s.index)
			ns = appendIndexToken(ns, idx)
			ext = appendIndexToken(ext, idx)
			if pointerOK {
				ptr = append(ptr, '/')
				ptr = append(ptr, idx...)
			}
			i := s.index
			segs = append(segs, PathSegment{Kind: SegmentIndex, Index: &i})

		case segKeyString:
			ns = appendIndexToken(ns, s.name)
			ext = appendIndexToken(ext, s.name)
			if pointerOK {
				ptr = append(ptr, '/')
				ptr = append(ptr, pointerEscaper.Replace(s.name)...)
			}
			segs = append(segs, PathSegment{Kind: SegmentMapKey, Key: s.name})

		case segKeyTyped:
			// non-string map keys get a typed path segment; no JSON Pointer
			// is fabricated for them.
			ns = appendIndexToken(ns, s.name)
			ext = appendIndexToken(ext, s.name)
			pointerOK = false
			segs = append(segs, PathSegment{Kind: SegmentTypedMapKey, Key: s.name, KeyType: s.keyType})

		case segUnresolved:
			ns = appendNameToken(ns, s.name)
			ext = appendNameToken(ext, s.altName)
			unresolved = true
			segs = append(segs, PathSegment{Kind: SegmentUnresolved, Field: s.name, Name: s.altName})
		}
	}

	goNS = strings.TrimSuffix(string(ns), ".")
	extPath = strings.TrimSuffix(string(ext), ".")
	if pointerOK && !unresolved {
		pointer = string(ptr)
	}

	return goNS, extPath, pointer, segs, unresolved
}

// appendNameToken appends "name." to the buffer.
func appendNameToken(b []byte, name string) []byte {
	b = append(b, name...)
	return append(b, '.')
}

// appendIndexToken appends "[token]." to the buffer, replacing the trailing
// dot of the previous token when present.
func appendIndexToken(b []byte, token string) []byte {
	if n := len(b); n > 0 && b[n-1] == '.' {
		b = b[:n-1]
	}
	b = append(b, '[')
	b = append(b, token...)
	b = append(b, ']')
	return append(b, '.')
}
