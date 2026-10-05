package validator

import "reflect"

// CollectionLevel contains read-only information about the collection
// (slice, array or map) currently being validated through a dive tag.
//
// A new CollectionLevel is pushed onto a per-validation stack every time
// the executor dives into a collection, so nested dives each have their
// own scope: a validator always sees the collection and index of the
// innermost dive that contains it, while Parent() exposes the enclosing
// dive's scope.
//
// The view is immutable: the Collection() reflect.Value is not settable
// and any attempt to modify it panics. The view is a snapshot taken on
// the first call to Collection(); it does not reflect modifications made
// to the underlying collection afterwards.
type CollectionLevel interface {

	// Collection returns a read-only view of the collection being dived.
	//
	// The returned reflect.Value is not addressable and not settable;
	// calling Set, SetInt, SetString, Slice on it (or on its elements)
	// panics. The view is a snapshot of the collection at the time of
	// the first call; later modifications to the underlying collection
	// are not reflected.
	Collection() reflect.Value

	// Index returns the index of the current element within Collection.
	// It returns -1 when there is no meaningful index, e.g. when diving
	// into a map.
	Index() int

	// Len returns the number of elements in the collection.
	Len() int

	// Parent returns the enclosing dive's CollectionLevel, or nil if
	// this is the outermost dive.
	Parent() CollectionLevel
}

// collectionLevel is the internal CollectionLevel implementation.
//
// Instances are stored on the per-validate collections stack; a fresh
// stack is used for every validation call, so concurrent validations
// never share or leak each other's scope.
type collectionLevel struct {
	collection reflect.Value // original slice/array/map used for reading
	frozen     reflect.Value // lazily-built non-settable view
	index      int           // current element index (-1 for maps)
	parent     *collectionLevel
}

var _ CollectionLevel = (*collectionLevel)(nil)

// Collection returns a read-only, non-settable view of the collection.
// The view is built lazily on the first call and cached for subsequent
// calls, so the (potentially costly) copy is only paid when a validator
// actually needs to inspect the collection.
func (c *collectionLevel) Collection() reflect.Value {
	if !c.frozen.IsValid() {
		c.frozen = freezeCollection(c.collection)
	}
	return c.frozen
}

// Index returns the index of the current element within the collection.
func (c *collectionLevel) Index() int {
	return c.index
}

// Len returns the number of elements in the collection.
func (c *collectionLevel) Len() int {
	return c.collection.Len()
}

// Parent returns the enclosing dive's scope, or nil for the outermost dive.
func (c *collectionLevel) Parent() CollectionLevel {
	if c.parent == nil {
		return nil
	}
	return c.parent
}

// freezeCollection returns a read-only, non-settable view of c.
//
// Arrays and maps are already non-settable when handed back through
// Interface(), so they are returned as-is. Slices always yield settable
// elements through reflect, so they are copied into a non-addressable
// array of the same length and element type; the resulting view supports
// Len, Index and Range like the original but rejects any modification
// with a panic.
//
// Deep mutation through reference-typed elements (e.g. a pointer inside
// a slice) is not prevented, just as with a regular copy; the guarantee
// is that the collection itself and its elements cannot be replaced
// through the returned view.
func freezeCollection(c reflect.Value) reflect.Value {
	switch c.Kind() {
	case reflect.Array, reflect.Map:
		// Hand back through Interface() to drop any addressability the
		// original value might have had.
		return reflect.ValueOf(c.Interface())
	default: // Slice
		n := c.Len()
		arr := reflect.New(reflect.ArrayOf(n, c.Type().Elem())).Elem()
		for i := 0; i < n; i++ {
			arr.Index(i).Set(c.Index(i))
		}
		return reflect.ValueOf(arr.Interface())
	}
}
