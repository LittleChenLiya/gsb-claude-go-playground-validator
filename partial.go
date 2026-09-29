package validator

import (
	"context"
	"reflect"
	"strings"
)

// siblingFieldDepTags are the baked-in validation tags whose parameter references a
// sibling field of the same struct. StructChanged tracks reverse dependencies for
// exactly these tags.
var siblingFieldDepTags = map[string]struct{}{
	"eqfield":  {},
	"nefield":  {},
	"gtfield":  {},
	"gtefield": {},
	"ltfield":  {},
	"ltefield": {},
}

// untrackedFieldRefTags are baked-in tags that also reference other fields but whose
// dependencies StructChanged cannot determine statically (cross-struct references or
// value-conditional rules). Their presence anywhere in the reachable struct's tags
// forces StructChanged to report that a full validation is required, so no
// dependency is ever silently missed.
var untrackedFieldRefTags = map[string]struct{}{
	"eqcsfield":           {},
	"necsfield":           {},
	"gtcsfield":           {},
	"gtecsfield":          {},
	"ltcsfield":           {},
	"ltecsfield":          {},
	requiredIfTag:         {},
	requiredUnlessTag:     {},
	requiredWithTag:       {},
	requiredWithAllTag:    {},
	requiredWithoutTag:    {},
	requiredWithoutAllTag: {},
	skipUnlessTag:         {},
	excludedIfTag:         {},
	excludedUnlessTag:     {},
	excludedWithTag:       {},
	excludedWithAllTag:    {},
	excludedWithoutTag:    {},
	excludedWithoutAllTag: {},
}

// StructChanged validates only the fields identified by changedFields plus every
// field whose validation outcome may depend on them, and reports whether a full
// Struct validation is required instead.
//
// changedFields are Go field paths relative to the provided struct, eg. "Start" or
// "Inner.Start". Reverse dependencies are derived from the baked-in sibling field
// comparison tags eqfield, nefield, gtfield, gtefield, ltfield and ltefield: if field
// B carries any of these rules referencing field A, changing A causes B to be
// (re)validated as well, transitively. Every affected field is validated at most
// once, so dependency cycles terminate.
//
// requiresFull is true (and err nil) when the dependency graph cannot be determined
// safely and the caller must fall back to Struct/StructCtx instead. This happens when:
//   - the struct, or any nested struct on a changed path, has a registered custom
//     struct-level validation,
//   - any reachable tag references fields in a way that cannot be tracked
//     (cross-struct references such as eqcsfield, conditional rules such as
//     required_if, or non-sibling parameters),
//   - a changed field path is unknown or cannot be resolved.
//
// It returns InvalidValidationError for bad values passed in and nil or
// ValidationErrors as error otherwise, exactly like Struct.
func (v *Validate) StructChanged(s interface{}, changedFields ...string) (err error, requiresFull bool) {
	return v.StructChangedCtx(context.Background(), s, changedFields...)
}

// StructChangedCtx does the same as StructChanged and allows passing of
// context.Context for contextual validation information.
func (v *Validate) StructChangedCtx(ctx context.Context, s interface{}, changedFields ...string) (err error, requiresFull bool) {
	val := reflect.ValueOf(s)

	if val.Kind() == reflect.Ptr && !val.IsNil() {
		val = val.Elem()
	}

	if val.Kind() != reflect.Struct || val.Type().ConvertibleTo(timeType) {
		return &InvalidValidationError{Type: reflect.TypeOf(s)}, false
	}

	if len(changedFields) == 0 {
		return nil, false
	}

	// pre-compute the reverse dependency graph of every reachable struct level;
	// any untrackable field reference anywhere forces a full validation so no
	// dependency is silently missed.
	deps, ok := v.collectDependencies(val.Type())
	if !ok {
		return nil, true
	}

	root := v.extractStructCache(val, val.Type().Name())
	if root.fn != nil {
		// custom struct-level validation: dependencies unknowable
		return nil, true
	}

	affected, ok := v.affectedFields(root, val, deps, changedFields)
	if !ok {
		return nil, true
	}

	if len(affected) == 0 {
		return nil, false
	}

	if err = v.StructPartialCtx(ctx, s, affected...); err != nil {
		return err, false
	}

	return nil, false
}

// changedField is a single resolved changed/affected field: the struct cache and
// value of the struct level it lives on, the Go path prefix of that level ("" at
// the root, eg. "Inner." one level down) and the Go field name. A nil cs marks a
// no-op entry, eg. a path crossing a nil pointer, which a full validation would
// skip as well.
type changedField struct {
	cs     *cStruct
	val    reflect.Value
	prefix string
	name   string
}

// affectedFields resolves the changed field paths and computes the transitive
// closure of fields whose validation may be affected. It returns ok=false when a
// full validation is required.
func (v *Validate) affectedFields(root *cStruct, rootVal reflect.Value, deps map[*cStruct]map[string][]string, changedFields []string) (affected []string, ok bool) {
	queue := make([]changedField, 0, len(changedFields))

	for _, path := range changedFields {
		cf, ok := v.resolveChangedField(root, rootVal, path)
		if !ok {
			return nil, false
		}
		queue = append(queue, cf)
	}

	// breadth-first traversal of the reverse dependency graph; the visited set
	// guarantees each field is validated at most once and cycles terminate.
	visited := make(map[string]struct{})
	// guards against pointer cycles in the data when expanding nested structs
	seenPtrs := make(map[visit]struct{})

	for len(queue) > 0 {
		cf := queue[0]
		queue = queue[1:]

		if cf.cs == nil {
			continue
		}

		full := cf.prefix + cf.name
		if _, seen := visited[full]; seen {
			continue
		}
		visited[full] = struct{}{}
		affected = append(affected, full)

		for _, dependent := range deps[cf.cs][cf.name] {
			if _, seen := visited[cf.prefix+dependent]; !seen {
				queue = append(queue, changedField{cs: cf.cs, val: cf.val, prefix: cf.prefix, name: dependent})
			}
		}

		// a changed struct field is revalidated as a whole; expand its subtree so
		// every nested field is included, pruning at nil pointers exactly like a
		// full validation would.
		cfield := findCField(cf.cs, cf.name)
		fldVal := cf.val.Field(cfield.idx)

		skip := false
		for fldVal.Kind() == reflect.Ptr {
			if fldVal.IsNil() {
				skip = true
				break
			}
			ptr := visit{typ: fldVal.Type(), ptr: fldVal.Pointer()}
			if _, seen := seenPtrs[ptr]; seen {
				skip = true
				break
			}
			seenPtrs[ptr] = struct{}{}
			fldVal = fldVal.Elem()
		}
		if skip {
			continue
		}

		nested, isNested := v.nestedStructType(fldVal.Type())
		if !isNested {
			continue
		}

		nestedCS := v.extractStructCache(fldVal, nested.Name())
		if nestedCS.fn != nil {
			// nested struct with custom struct-level validation
			return nil, false
		}

		for _, f := range nestedCS.fields {
			queue = append(queue, changedField{cs: nestedCS, val: fldVal, prefix: full + namespaceSeparator, name: f.name})
		}
	}

	return affected, true
}

// visit identifies a pointer in the data graph to detect cycles while expanding
// nested structs.
type visit struct {
	typ reflect.Type
	ptr uintptr
}

// resolveChangedField resolves a single Go field path (eg. "Inner.Start") against
// the struct cache, descending into nested structs. It returns ok=false when the
// path is unknown, crosses an unresolvable boundary (slices, maps, interfaces,
// custom types, time.Time) or a nested struct on the path has a custom struct-level
// validation. Paths crossing a nil pointer resolve to a no-op entry.
func (v *Validate) resolveChangedField(root *cStruct, rootVal reflect.Value, path string) (changedField, bool) {
	cs := root
	val := rootVal
	prefix := ""
	segments := strings.Split(path, namespaceSeparator)

	for i, seg := range segments {
		if len(seg) == 0 || strings.ContainsAny(seg, leftBracket+rightBracket) {
			return changedField{}, false
		}

		cf := findCField(cs, seg)
		if cf == nil {
			// unknown field path; cannot determine what depends on it
			return changedField{}, false
		}

		if i == len(segments)-1 {
			return changedField{cs: cs, val: val, prefix: prefix, name: seg}, true
		}

		// intermediate segment: must be a nested struct we can descend into
		fldVal := val.Field(cf.idx)
		nested, ok := v.nestedStructType(fldVal.Type())
		if !ok {
			return changedField{}, false
		}

		for fldVal.Kind() == reflect.Ptr {
			if fldVal.IsNil() {
				// a full validation would not descend either; nothing to do
				return changedField{}, true
			}
			fldVal = fldVal.Elem()
		}

		cs = v.extractStructCache(reflect.New(nested).Elem(), nested.Name())
		if cs.fn != nil {
			// nested struct with custom struct-level validation
			return changedField{}, false
		}
		val = fldVal
		prefix += seg + namespaceSeparator
	}

	return changedField{}, false
}

// nestedStructType unwraps pointers and reports whether the type is a plain nested
// struct StructChanged can descend into (not time.Time, not a custom type).
func (v *Validate) nestedStructType(typ reflect.Type) (reflect.Type, bool) {
	for typ.Kind() == reflect.Ptr {
		typ = typ.Elem()
	}

	if typ.Kind() != reflect.Struct || typ.ConvertibleTo(timeType) {
		return nil, false
	}

	if v.hasCustomFuncs {
		if _, ok := v.customFuncs[typ]; ok {
			return nil, false
		}
	}

	return typ, true
}

// findCField returns the cField with the given Go field name, or nil.
func findCField(cs *cStruct, name string) *cField {
	for _, f := range cs.fields {
		if f.name == name {
			return f
		}
	}
	return nil
}

// collectDependencies walks every struct type reachable from rootTyp and builds the
// per-level reverse dependency graph. It returns ok=false when any reachable tag
// references fields in a way that cannot be tracked, in which case a full
// validation is required.
func (v *Validate) collectDependencies(rootTyp reflect.Type) (map[*cStruct]map[string][]string, bool) {
	deps := make(map[*cStruct]map[string][]string)
	visited := make(map[reflect.Type]struct{})

	var walk func(typ reflect.Type) bool
	walk = func(typ reflect.Type) bool {
		for typ.Kind() == reflect.Ptr || typ.Kind() == reflect.Slice || typ.Kind() == reflect.Array {
			typ = typ.Elem()
		}

		switch typ.Kind() {
		case reflect.Interface:
			// the runtime type is unknowable; it may hide structs with untracked
			// field references
			return false

		case reflect.Map:
			return walk(typ.Key()) && walk(typ.Elem())

		case reflect.Struct:
			if typ.ConvertibleTo(timeType) {
				return true
			}

			if v.hasCustomFuncs {
				if _, ok := v.customFuncs[typ]; ok {
					return true
				}
			}

			if _, seen := visited[typ]; seen {
				return true
			}
			visited[typ] = struct{}{}

			cs := v.extractStructCache(reflect.New(typ).Elem(), typ.Name())

			reverse, ok := siblingDependencies(cs)
			if !ok {
				return false
			}
			deps[cs] = reverse

			for _, f := range cs.fields {
				if !walk(typ.Field(f.idx).Type) {
					return false
				}
			}
			return true

		default:
			return true
		}
	}

	if !walk(rootTyp) {
		return nil, false
	}

	return deps, true
}

// siblingDependencies builds the reverse dependency graph for one struct level from
// the baked-in sibling field comparison tags: reverse[x] lists the fields whose
// validation rules reference x. It returns ok=false when any tag references fields
// in a way that cannot be tracked, in which case a full validation is required.
func siblingDependencies(cs *cStruct) (reverse map[string][]string, ok bool) {
	fieldNames := make(map[string]struct{}, len(cs.fields))
	for _, f := range cs.fields {
		fieldNames[f.name] = struct{}{}
	}

	reverse = make(map[string][]string)

	for _, f := range cs.fields {
		if !tagChainTrackable(f.cTags, f.name, fieldNames, reverse) {
			return nil, false
		}
	}

	return reverse, true
}

// tagChainTrackable walks one field's parsed tag chain recording sibling field
// dependencies into reverse. It returns false when the chain references fields in
// an untrackable way.
func tagChainTrackable(ct *cTag, fieldName string, fieldNames map[string]struct{}, reverse map[string][]string) bool {
	for dived := false; ct != nil; ct = ct.next {
		switch ct.typeof {
		case typeDive, typeKeys:
			// tags inside a dive/keys subtree apply to elements, not to siblings of
			// this struct level; dependencies there cannot be mapped to this level.
			dived = true
			if ct.typeof == typeKeys && ct.keys != nil {
				if !tagChainTrackable(ct.keys, fieldName, fieldNames, reverse) {
					return false
				}
			}
			continue
		}

		if _, untracked := untrackedFieldRefTags[ct.tag]; untracked {
			return false
		}

		if _, isDep := siblingFieldDepTags[ct.tag]; !isDep {
			continue
		}

		if dived {
			// field comparison inside a dive subtree
			return false
		}

		// only plain sibling references are trackable
		if strings.ContainsAny(ct.param, namespaceSeparator+leftBracket+rightBracket) {
			return false
		}

		if _, known := fieldNames[ct.param]; !known {
			return false
		}

		reverse[ct.param] = append(reverse[ct.param], fieldName)
	}

	return true
}
