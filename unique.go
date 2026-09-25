package validator

import (
	"encoding/binary"
	"fmt"
	"hash"
	"hash/fnv"
	"reflect"
	"strconv"
	"strings"
)

const uniqueFieldsTag = "uniquefields"

// UniqueNilPolicy controls how nil field values (eg. nil pointers) are
// treated when computing the composite key of a collection element.
type UniqueNilPolicy int

const (
	// UniqueNilSkip skips any element for which at least one of the
	// configured fields is nil; such elements never take part in the
	// uniqueness comparison.
	UniqueNilSkip UniqueNilPolicy = iota

	// UniqueNilAsValue treats nil as a regular value participating in the
	// composite key; two elements whose fields are nil at the same
	// positions are considered duplicates of each other.
	UniqueNilAsValue
)

// uniqueValueKind identifies the typed kind of a single component of a
// composite unique key.
type uniqueValueKind uint8

const (
	uvNil uniqueValueKind = iota
	uvString
	uvInt
	uvUint
	uvBool
)

// uniqueValue is a single typed component of a composite unique key.
// Components are compared using the equality semantics of their own type,
// never via string concatenation.
type uniqueValue struct {
	kind uniqueValueKind
	s    string
	i    int64
	u    uint64
	b    bool
}

// uniqueEntry records the first-seen composite key and its element index.
type uniqueEntry struct {
	tuple []uniqueValue
	index int
}

// uniqueRule is a compiled struct-level composite uniqueness rule.
type uniqueRule struct {
	collectionPath string
	collectionIdx  []int
	fieldPaths     []string
	fieldIdx       [][]int
	nilPolicy      UniqueNilPolicy
	param          string
}

// RegisterStructLevelUnique registers a struct-level composite uniqueness
// rule against the provided struct types.
//
// collectionPath is a dot-separated path, relative to each registered
// struct type, resolving to a slice or array of structs (pointers along the
// path, including the elements themselves, are traversed automatically).
//
// fieldPaths are dot-separated paths, relative to the collection element
// type, whose leaf values form the composite key. Leaves must resolve to
// string, any signed or unsigned integer, or boolean (named types
// included); pointers are traversed and nils are handled according to
// nilPolicy.
//
// When two or more elements share the same composite key an error is
// reported for both the first occurrence and every duplicate under the
// 'uniquefields' tag. Only field paths and element indexes are recorded in
// the error; the composite key contents never appear in the error.
//
// Registration fails with an error when the collection path or any field
// path is missing, unexported, or resolves to an unsupported type
// (eg. floats, complex numbers, slices, maps, ...).
//
// NOTE: this method is not thread-safe it is intended that these all be
// registered prior to any validation.
func (v *Validate) RegisterStructLevelUnique(collectionPath string, fieldPaths []string, nilPolicy UniqueNilPolicy, types ...interface{}) error {
	if nilPolicy != UniqueNilSkip && nilPolicy != UniqueNilAsValue {
		return fmt.Errorf("validator: invalid UniqueNilPolicy %d", nilPolicy)
	}

	if len(collectionPath) == 0 {
		return fmt.Errorf("validator: unique collection path cannot be empty")
	}

	if len(fieldPaths) == 0 {
		return fmt.Errorf("validator: unique field paths cannot be empty")
	}

	for _, t := range types {
		typ := reflect.TypeOf(t)
		if typ == nil {
			return fmt.Errorf("validator: unique rule cannot be registered against a nil type")
		}

		for typ.Kind() == reflect.Ptr {
			typ = typ.Elem()
		}

		if typ.Kind() != reflect.Struct {
			return fmt.Errorf("validator: unique rule can only be registered against structs, got %s", typ)
		}

		rule, err := compileUniqueRule(collectionPath, fieldPaths, nilPolicy, typ)
		if err != nil {
			return err
		}

		if v.uniqueRules == nil {
			v.uniqueRules = make(map[reflect.Type][]*uniqueRule)
		}

		v.uniqueRules[typ] = append(v.uniqueRules[typ], rule)
	}

	return nil
}

// compileUniqueRule resolves and validates the collection and field paths
// against the provided struct type.
func compileUniqueRule(collectionPath string, fieldPaths []string, nilPolicy UniqueNilPolicy, typ reflect.Type) (*uniqueRule, error) {
	collectionIdx, collType, err := resolveUniqueFieldChain(typ, collectionPath)
	if err != nil {
		return nil, err
	}

	for collType.Kind() == reflect.Ptr {
		collType = collType.Elem()
	}

	if collType.Kind() != reflect.Slice && collType.Kind() != reflect.Array {
		return nil, fmt.Errorf("validator: unique collection path %q must resolve to a slice or array, got %s", collectionPath, collType)
	}

	elemType := collType.Elem()
	for elemType.Kind() == reflect.Ptr {
		elemType = elemType.Elem()
	}

	if elemType.Kind() != reflect.Struct {
		return nil, fmt.Errorf("validator: unique collection path %q elements must be structs, got %s", collectionPath, elemType)
	}

	rule := &uniqueRule{
		collectionPath: collectionPath,
		collectionIdx:  collectionIdx,
		fieldPaths:     append([]string(nil), fieldPaths...),
		fieldIdx:       make([][]int, len(fieldPaths)),
		nilPolicy:      nilPolicy,
		param:          strings.Join(fieldPaths, ","),
	}

	for i, fieldPath := range fieldPaths {
		chain, leafType, err := resolveUniqueFieldChain(elemType, fieldPath)
		if err != nil {
			return nil, err
		}

		for leafType.Kind() == reflect.Ptr {
			leafType = leafType.Elem()
		}

		switch leafType.Kind() {
		case reflect.String, reflect.Bool,
			reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
			reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		default:
			return nil, fmt.Errorf("validator: unique field path %q resolves to unsupported type %s", fieldPath, leafType)
		}

		rule.fieldIdx[i] = chain
	}

	return rule, nil
}

// resolveUniqueFieldChain resolves a dot-separated field path against typ,
// traversing pointers and embedded structs, returning the field index chain
// and the resolved leaf type.
func resolveUniqueFieldChain(typ reflect.Type, path string) ([]int, reflect.Type, error) {
	var chain []int

	current := typ

	for _, name := range strings.Split(path, namespaceSeparator) {
		for current.Kind() == reflect.Ptr {
			current = current.Elem()
		}

		if current.Kind() != reflect.Struct {
			return nil, nil, fmt.Errorf("validator: unique field path %q traverses non-struct type %s", path, current)
		}

		field, ok := current.FieldByName(name)
		if !ok {
			return nil, nil, fmt.Errorf("validator: unique field path %q: field %q not found in %s", path, name, current)
		}

		if field.PkgPath != "" {
			return nil, nil, fmt.Errorf("validator: unique field path %q: field %q is unexported", path, name)
		}

		chain = append(chain, field.Index...)
		current = field.Type
	}

	return chain, current, nil
}

// check runs the compiled rule against the current struct value, reporting
// an error for the first occurrence and every duplicate of each repeated
// composite key. Element order is never modified.
func (rule *uniqueRule) check(v *validate, current reflect.Value) {
	collection := current

	for _, idx := range rule.collectionIdx {
		for collection.Kind() == reflect.Ptr {
			if collection.IsNil() {
				return
			}
			collection = collection.Elem()
		}
		collection = collection.Field(idx)
	}

	for collection.Kind() == reflect.Ptr {
		if collection.IsNil() {
			return
		}
		collection = collection.Elem()
	}

	if collection.Kind() != reflect.Slice && collection.Kind() != reflect.Array {
		return
	}

	seen := make(map[uint64][]uniqueEntry)
	reportedFirst := make(map[int]bool)
	hasher := fnv.New64a()

	for i := 0; i < collection.Len(); i++ {
		elem := collection.Index(i)

		nilElem := false
		for elem.Kind() == reflect.Ptr {
			if elem.IsNil() {
				nilElem = true
				break
			}
			elem = elem.Elem()
		}

		if nilElem || elem.Kind() != reflect.Struct {
			continue
		}

		tuple := make([]uniqueValue, len(rule.fieldIdx))
		skip := false

		for j, chain := range rule.fieldIdx {
			tuple[j] = extractUniqueValue(elem, chain)

			if tuple[j].kind == uvNil && rule.nilPolicy == UniqueNilSkip {
				skip = true
				break
			}
		}

		if skip {
			continue
		}

		sum := hashUniqueTuple(hasher, tuple)

		first := -1
		for _, entry := range seen[sum] {
			if uniqueTuplesEqual(entry.tuple, tuple) {
				first = entry.index
				break
			}
		}

		if first == -1 {
			seen[sum] = append(seen[sum], uniqueEntry{tuple: tuple, index: i})
			continue
		}

		if !reportedFirst[first] {
			reportedFirst[first] = true
			rule.report(v, first)
		}

		rule.report(v, i)
	}
}

// extractUniqueValue walks the field index chain, traversing pointers, and
// returns the typed leaf value. A nil pointer anywhere along the chain
// yields a uvNil value.
func extractUniqueValue(elem reflect.Value, chain []int) uniqueValue {
	current := elem

	for _, idx := range chain {
		for current.Kind() == reflect.Ptr {
			if current.IsNil() {
				return uniqueValue{kind: uvNil}
			}
			current = current.Elem()
		}
		current = current.Field(idx)
	}

	for current.Kind() == reflect.Ptr {
		if current.IsNil() {
			return uniqueValue{kind: uvNil}
		}
		current = current.Elem()
	}

	switch current.Kind() {
	case reflect.String:
		return uniqueValue{kind: uvString, s: current.String()}
	case reflect.Bool:
		return uniqueValue{kind: uvBool, b: current.Bool()}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return uniqueValue{kind: uvInt, i: current.Int()}
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return uniqueValue{kind: uvUint, u: current.Uint()}
	default:
		// unreachable: leaf kinds are validated at registration time
		return uniqueValue{kind: uvNil}
	}
}

// hashUniqueTuple hashes a composite key component by component, preserving
// each component's type and length, so that eg. ("a", "bc") and ("ab", "c")
// can never collide.
func hashUniqueTuple(hasher hash.Hash64, tuple []uniqueValue) uint64 {
	var buf [8]byte

	hasher.Reset()

	for _, value := range tuple {
		buf[0] = byte(value.kind)
		_, _ = hasher.Write(buf[:1])

		switch value.kind {
		case uvString:
			binary.LittleEndian.PutUint64(buf[:8], uint64(len(value.s)))
			_, _ = hasher.Write(buf[:8])
			_, _ = hasher.Write([]byte(value.s))
		case uvInt:
			binary.LittleEndian.PutUint64(buf[:8], uint64(value.i))
			_, _ = hasher.Write(buf[:8])
		case uvUint:
			binary.LittleEndian.PutUint64(buf[:8], value.u)
			_, _ = hasher.Write(buf[:8])
		case uvBool:
			if value.b {
				buf[0] = 1
			} else {
				buf[0] = 0
			}
			_, _ = hasher.Write(buf[:1])
		case uvNil:
		}
	}

	return hasher.Sum64()
}

// uniqueTuplesEqual reports whether two composite keys are component-wise
// equal using each component's own type semantics.
func uniqueTuplesEqual(a, b []uniqueValue) bool {
	if len(a) != len(b) {
		return false
	}

	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}

	return true
}

// report appends an error pointing at the collection element with the given
// index. Only the field path and element index are recorded; the composite
// key contents are never included.
func (rule *uniqueRule) report(v *validate, index int) {
	fieldName := rule.collectionPath + leftBracket + strconv.Itoa(index) + rightBracket

	v.errs = append(v.errs,
		&fieldError{
			v:              v.v,
			tag:            uniqueFieldsTag,
			actualTag:      uniqueFieldsTag,
			ns:             string(append(v.ns, fieldName...)),
			structNs:       string(append(v.actualNs, fieldName...)),
			fieldLen:       uint8(len(fieldName)),
			structfieldLen: uint8(len(fieldName)),
			param:          rule.param,
			kind:           reflect.Slice,
		},
	)
}
