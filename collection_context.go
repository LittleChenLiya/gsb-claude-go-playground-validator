package validator

import (
	"errors"
	"reflect"
)

// ErrCollectionViewReadOnly is returned by any attempt to mutate a
// CollectionView. A CollectionView is a read-only identity view over the
// collection currently being validated and never permits modification of
// the original slice, array or map.
var ErrCollectionViewReadOnly = errors.New("validator: collection view is read-only")

// CollectionView is a read-only identity view over the collection
// (slice, array or map) the validator is currently diving into.
//
// The view refers to the original collection - it is not a copy - so its
// length and elements reflect the collection at call time, allowing custom
// validation functions to inspect sibling elements without resorting to
// global state. It never permits mutation: mutating operations return
// ErrCollectionViewReadOnly and element values handed out are not settable.
//
// The zero value is safe to use but reports no collection.
type CollectionView struct {
	val reflect.Value
}

// IsValid reports whether the view refers to an actual collection.
func (cv CollectionView) IsValid() bool {
	return cv.val.IsValid()
}

// Kind returns the kind of the underlying collection (reflect.Slice,
// reflect.Array or reflect.Map) or reflect.Invalid for the zero view.
func (cv CollectionView) Kind() reflect.Kind {
	if !cv.val.IsValid() {
		return reflect.Invalid
	}
	return cv.val.Kind()
}

// Len returns the number of elements in the underlying collection, or 0
// for the zero view.
func (cv CollectionView) Len() int {
	if !cv.val.IsValid() {
		return 0
	}
	return cv.val.Len()
}

// Index returns the i-th element of the underlying slice or array as a
// read-only reflect.Value. It returns the zero Value for maps and the zero
// view; use MapIndex for map collections.
//
// The returned Value can be inspected (Interface, Kind, ...) but is never
// settable, ensuring the original collection cannot be modified through
// the view.
func (cv CollectionView) Index(i int) reflect.Value {
	if !cv.val.IsValid() || cv.val.Kind() == reflect.Map {
		return reflect.Value{}
	}
	return readOnlyValue(cv.val.Index(i))
}

// MapIndex returns the value associated with key in the underlying map as
// a read-only reflect.Value. It returns the zero Value for slice/array
// collections, the zero view, or when the key is not present.
func (cv CollectionView) MapIndex(key reflect.Value) reflect.Value {
	if !cv.val.IsValid() || cv.val.Kind() != reflect.Map {
		return reflect.Value{}
	}
	// map index values are never settable, already read-only
	return cv.val.MapIndex(key)
}

// Set always fails with ErrCollectionViewReadOnly: a CollectionView is a
// read-only view and never permits modification of the underlying
// collection.
func (cv CollectionView) Set(i int, value interface{}) error {
	return ErrCollectionViewReadOnly
}

// readOnlyValue returns a reflect.Value holding the same value as v but
// guaranteed to be unsettable, so the original collection element cannot
// be modified through it.
func readOnlyValue(v reflect.Value) reflect.Value {
	if !v.IsValid() || !v.CanSet() {
		// invalid values and values already read-only (e.g. obtained from
		// unexported fields or maps) need no protection
		return v
	}
	if v.CanInterface() {
		if rv := reflect.ValueOf(v.Interface()); rv.IsValid() {
			return rv
		}
		// e.g. a nil interface element: hand out a typed, unsettable zero
		return reflect.Zero(v.Type())
	}
	return v
}

// collectionScope tracks the collection currently being dived into and the
// index of the element under validation.
//
// Scopes form a stack held by the per-request validate state: every nested
// 'dive' pushes its own scope which is popped when the dive completes, so
// nested collections never leak into each other and concurrent validations
// (each running on its own pooled validate state) never share scopes.
type collectionScope struct {
	coll     reflect.Value
	index    int
	hasIndex bool
}

// pushCollectionScope enters a new collection scope for a dive.
func (v *validate) pushCollectionScope(coll reflect.Value, hasIndex bool) {
	v.collStack = append(v.collStack, collectionScope{coll: coll, index: -1, hasIndex: hasIndex})
}

// popCollectionScope leaves the innermost collection scope when a dive
// completes, restoring any enclosing scope.
func (v *validate) popCollectionScope() {
	v.collStack = v.collStack[:len(v.collStack)-1]
}

// setCollectionIndex records the index of the element currently being
// validated within the innermost collection scope.
func (v *validate) setCollectionIndex(i int) {
	v.collStack[len(v.collStack)-1].index = i
}

// Collection returns a read-only view of the innermost collection
// (slice, array or map) currently being dived into, allowing custom
// validation functions to inspect sibling elements.
//
// ok is false when not validating within a collection dive or when
// collection context tracking is not enabled (see WithCollectionContext).
func (v *validate) Collection() (view CollectionView, ok bool) {
	if len(v.collStack) == 0 {
		return CollectionView{}, false
	}
	return CollectionView{val: v.collStack[len(v.collStack)-1].coll}, true
}

// CollectionIndex returns the index of the element currently being
// validated within the innermost slice or array being dived into.
//
// ok is false when not validating within a slice/array dive (including
// map dives, which have no positional index) or when collection context
// tracking is not enabled (see WithCollectionContext).
func (v *validate) CollectionIndex() (index int, ok bool) {
	if len(v.collStack) == 0 {
		return 0, false
	}
	s := v.collStack[len(v.collStack)-1]
	return s.index, s.hasIndex
}
