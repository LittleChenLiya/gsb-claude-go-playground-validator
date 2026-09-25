package validator

import (
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
)

// compositeUniqueTag is the tag reported on FieldError's produced by
// composite uniqueness rules. It can be used with RegisterTranslation
// to provide custom error translations.
const compositeUniqueTag = "composite_unique"

// CompositeUniqueNilPolicy defines how nil values encountered while
// resolving a composite unique key field are treated.
type CompositeUniqueNilPolicy int

const (
	// CompositeUniqueSkipNil skips any collection element for which at
	// least one key field resolves to nil; such elements never
	// participate in uniqueness checks.
	CompositeUniqueSkipNil CompositeUniqueNilPolicy = iota

	// CompositeUniqueNilAsValue treats nil as a regular key value, so
	// two elements whose key fields are nil in the same positions are
	// considered duplicates of each other.
	CompositeUniqueNilAsValue
)

// CompositeUniqueOption customizes a composite unique rule.
type CompositeUniqueOption func(*compositeUniqueConfig)

type compositeUniqueConfig struct {
	nilPolicy CompositeUniqueNilPolicy
}

// WithCompositeUniqueNilPolicy sets the nil handling policy of a
// composite unique rule. When not specified the rule defaults to
// CompositeUniqueSkipNil.
func WithCompositeUniqueNilPolicy(policy CompositeUniqueNilPolicy) CompositeUniqueOption {
	return func(c *compositeUniqueConfig) {
		c.nilPolicy = policy
	}
}

// cuNil is the sentinel key component used for nil values when the
// CompositeUniqueNilAsValue policy is in effect.
type cuNil struct{}

// compositeUniqueField is a single resolved key field of a composite
// unique rule.
type compositeUniqueField struct {
	path    string // original field path, e.g. "Address.City"
	indices []int  // struct field index chain from the element type
}

// extract resolves the field value within a collection element.
// It dereferences any pointers along the chain; the second return
// value reports whether a nil pointer was encountered.
func (f *compositeUniqueField) extract(elem reflect.Value) (interface{}, bool) {
	fv := elem
	for _, idx := range f.indices {
		for fv.Kind() == reflect.Ptr {
			if fv.IsNil() {
				return nil, true
			}
			fv = fv.Elem()
		}
		fv = fv.Field(idx)
	}
	for fv.Kind() == reflect.Ptr {
		if fv.IsNil() {
			return nil, true
		}
		fv = fv.Elem()
	}

	// normalize named types to their underlying base value so that
	// equality uses each type's own semantics
	switch fv.Kind() {
	case reflect.String:
		return fv.String(), false
	case reflect.Bool:
		return fv.Bool(), false
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return fv.Int(), false
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return fv.Uint(), false
	}

	// unreachable: kinds are validated at registration time
	return nil, true
}

// compositeUniqueRule is a parsed, ready to run composite unique rule.
type compositeUniqueRule struct {
	collectionIndices []int  // struct field index chain to the collection
	collectionDisplay string // display path of the collection, e.g. "Tenants"
	fields            []compositeUniqueField
	param             string // comma separated field paths, recorded on FieldError.Param()
	nilPolicy         CompositeUniqueNilPolicy
}

// RegisterCompositeUnique registers a struct level composite uniqueness
// rule for the type of sample (a struct or pointer to struct).
//
// collectionPath is a dot separated path of field names, starting at the
// type of sample, resolving to a slice or array of structs (pointers are
// traversed). fieldPaths are dot separated paths of field names resolved
// within the collection element type; pointers and nested structs may be
// traversed. Only string, integer (signed and unsigned) and boolean
// fields are supported as key fields, including named types based on
// them; equality uses each type's own semantics and never string
// concatenation, so tuples like ("a", "bc") and ("ab", "c") are distinct.
//
// When the struct is validated, elements whose key field tuples are
// duplicated produce one FieldError (tag "composite_unique") for the
// first occurrence and one for every duplicate, each pointing at the
// collection element (e.g. "Tenants[1]"). Only field paths and element
// indexes are recorded; the composite key contents never appear in the
// error. Element order is never modified and the rule coexists with
// regular field tags and the "dive" tag.
//
// Registration fails with an error when the collection path or any field
// path cannot be resolved, or when a key field has an unsupported type.
func (v *Validate) RegisterCompositeUnique(sample interface{}, collectionPath string, fieldPaths []string, opts ...CompositeUniqueOption) error {
	cfg := compositeUniqueConfig{nilPolicy: CompositeUniqueSkipNil}
	for _, opt := range opts {
		opt(&cfg)
	}

	if len(fieldPaths) == 0 {
		return errors.New("validator: composite unique rule requires at least one field path")
	}

	typ := reflect.TypeOf(sample)
	if typ == nil {
		return errors.New("validator: composite unique sample cannot be nil")
	}
	for typ.Kind() == reflect.Ptr {
		typ = typ.Elem()
	}
	if typ.Kind() != reflect.Struct {
		return fmt.Errorf("validator: composite unique sample must be a struct or pointer to struct, got %s", typ.Kind())
	}

	rule := &compositeUniqueRule{
		collectionDisplay: collectionPath,
		param:             strings.Join(fieldPaths, ","),
		nilPolicy:         cfg.nilPolicy,
	}

	// resolve the collection path
	collType := typ
	for _, segment := range strings.Split(collectionPath, ".") {
		for collType.Kind() == reflect.Ptr {
			collType = collType.Elem()
		}
		if collType.Kind() != reflect.Struct {
			return fmt.Errorf("validator: composite unique collection path %q traverses non-struct type %s", collectionPath, collType)
		}
		field, ok := collType.FieldByName(segment)
		if !ok {
			return fmt.Errorf("validator: composite unique collection path %q: field %q not found on %s", collectionPath, segment, collType)
		}
		rule.collectionIndices = append(rule.collectionIndices, field.Index...)
		collType = field.Type
	}
	for collType.Kind() == reflect.Ptr {
		collType = collType.Elem()
	}
	if collType.Kind() != reflect.Slice && collType.Kind() != reflect.Array {
		return fmt.Errorf("validator: composite unique collection path %q must resolve to a slice or array, got %s", collectionPath, collType.Kind())
	}

	elemType := collType.Elem()
	for elemType.Kind() == reflect.Ptr {
		elemType = elemType.Elem()
	}
	if elemType.Kind() != reflect.Struct {
		return fmt.Errorf("validator: composite unique collection %q elements must be structs, got %s", collectionPath, elemType.Kind())
	}

	// resolve each key field path against the element type
	for _, path := range fieldPaths {
		cf := compositeUniqueField{path: path}
		ft := elemType
		for _, segment := range strings.Split(path, ".") {
			for ft.Kind() == reflect.Ptr {
				ft = ft.Elem()
			}
			if ft.Kind() != reflect.Struct {
				return fmt.Errorf("validator: composite unique field path %q traverses non-struct type %s", path, ft)
			}
			field, ok := ft.FieldByName(segment)
			if !ok {
				return fmt.Errorf("validator: composite unique field path %q: field %q not found on %s", path, segment, ft)
			}
			cf.indices = append(cf.indices, field.Index...)
			ft = field.Type
		}
		for ft.Kind() == reflect.Ptr {
			ft = ft.Elem()
		}
		switch ft.Kind() {
		case reflect.String, reflect.Bool,
			reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
			reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		default:
			return fmt.Errorf("validator: composite unique field path %q has unsupported type %s (only string, integer and boolean fields are supported)", path, ft)
		}
		rule.fields = append(rule.fields, cf)
	}

	if v.compositeUniqueRules == nil {
		v.compositeUniqueRules = make(map[reflect.Type][]*compositeUniqueRule)
	}
	v.compositeUniqueRules[typ] = append(v.compositeUniqueRules[typ], rule)
	return nil
}

// validate runs the rule against the struct value current, which is
// guaranteed to be of the type the rule was registered for.
func (r *compositeUniqueRule) validate(v *validate, current reflect.Value) {
	coll := current
	for _, idx := range r.collectionIndices {
		for coll.Kind() == reflect.Ptr {
			if coll.IsNil() {
				return
			}
			coll = coll.Elem()
		}
		coll = coll.Field(idx)
	}
	for coll.Kind() == reflect.Ptr {
		if coll.IsNil() {
			return
		}
		coll = coll.Elem()
	}

	length := coll.Len()
	if length == 0 {
		return
	}

	// nested per-field maps keyed by each field's own typed value;
	// leaf values are the index of the first element carrying the tuple
	seen := make(map[interface{}]interface{})
	reported := make(map[int]bool, length)

	for i := 0; i < length; i++ {
		elem := coll.Index(i)

		vals := make([]interface{}, len(r.fields))
		skip := false
		for j := range r.fields {
			val, isNil := r.fields[j].extract(elem)
			if isNil {
				if r.nilPolicy == CompositeUniqueSkipNil {
					skip = true
					break
				}
				vals[j] = cuNil{}
			} else {
				vals[j] = val
			}
		}
		if skip {
			continue
		}

		node := seen
		for _, val := range vals[:len(vals)-1] {
			next, ok := node[val]
			if !ok {
				child := make(map[interface{}]interface{})
				node[val] = child
				node = child
			} else {
				node = next.(map[interface{}]interface{})
			}
		}

		last := vals[len(vals)-1]
		if first, ok := node[last]; ok {
			firstIdx := first.(int)
			if !reported[firstIdx] {
				reported[firstIdx] = true
				r.report(v, firstIdx)
			}
			reported[i] = true
			r.report(v, i)
		} else {
			node[last] = i
		}
	}
}

// report appends a FieldError pointing at the collection element with
// the given index. Only the field path and element index are recorded;
// the composite key contents are intentionally not included.
func (r *compositeUniqueRule) report(v *validate, index int) {
	name := r.collectionDisplay + "[" + strconv.Itoa(index) + "]"

	v.str1 = string(append(v.ns, name...))
	if v.v.hasTagNameFunc {
		v.str2 = string(append(v.actualNs, name...))
	} else {
		v.str2 = v.str1
	}

	v.errs = append(v.errs,
		&fieldError{
			v:              v.v,
			tag:            compositeUniqueTag,
			actualTag:      compositeUniqueTag,
			ns:             v.str1,
			structNs:       v.str2,
			fieldLen:       uint8(len(name)),
			structfieldLen: uint8(len(name)),
			param:          r.param,
			kind:           reflect.Invalid,
		},
	)
}
