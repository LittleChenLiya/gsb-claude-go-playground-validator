package validator

import "reflect"

// FieldPathSegmentKind describes how a single FieldPathSegment relates to the
// value being validated.
type FieldPathSegmentKind uint8

const (
	// FieldPathSegmentStruct identifies a segment produced by a (possibly
	// anonymous or nested) struct field.
	FieldPathSegmentStruct FieldPathSegmentKind = iota

	// FieldPathSegmentIndex identifies a segment produced by a slice or array
	// index while traversing a `dive` tag.
	FieldPathSegmentIndex

	// FieldPathSegmentKey identifies a segment produced by a map key while
	// traversing a `dive` tag.
	FieldPathSegmentKey
)

// String returns a short textual description of the segment kind.
func (k FieldPathSegmentKind) String() string {
	switch k {
	case FieldPathSegmentStruct:
		return "struct"
	case FieldPathSegmentIndex:
		return "index"
	case FieldPathSegmentKey:
		return "key"
	default:
		return "unknown"
	}
}

// FieldPathSegment is one unambiguous piece of a field path.
//
// Name is the original Go field name, or the textual index/key (without the
// surrounding brackets) for slice and map traversal.
// DisplayName is the name decided by the FieldNameFunc for this validation
// call; it falls back to Name when no custom name was provided.
// Because DisplayName is allowed to contain '.' and/or '[]', callers must use
// the slice of segments rather than splitting Namespace/DisplayNamespace to
// process a path without ambiguity.
type FieldPathSegment struct {
	// Kind is the kind of this path segment.
	Kind FieldPathSegmentKind

	// Name is the original Go field name, or the textual index/key for slice
	// and map traversal.
	Name string

	// DisplayName is the display name for this validation call; it equals Name
	// when the FieldNameFunc did not provide one.
	DisplayName string
}

// FieldInfo carries the struct field information handed to a FieldNameFunc so
// that it can decide the display name for a single validation call.
type FieldInfo struct {
	// Struct is the type of the struct the field is declared on.
	Struct reflect.Type

	// Field is the reflected struct field. For anonymous embedded structs
	// Field.Anonymous is true.
	Field reflect.StructField

	// Index is the field's index within Struct.
	Index int

	// Path is the original Go field path walked so far, starting at the top
	// level struct name, for example "User.Addresses[0].Street".
	Path string
}

// FieldNameFunc decides the display name of a struct field for one validation
// call only. It never changes the validator's global configuration or the
// per-type caches and may safely be different for every invocation.
//
// Return the desired display name, or an empty string to keep the default
// alternate name (the one derived from the global RegisterTagNameFunc or the
// Go field name). The function is invoked for every struct field actually
// walked, including the fields of anonymous embedded structs; slices, arrays
// and maps append their index/key segments automatically, including the
// surrounding brackets.
type FieldNameFunc func(info FieldInfo) string
