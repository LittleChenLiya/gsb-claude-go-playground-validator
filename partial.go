package validator

import (
	"context"
	"reflect"
	"sort"
	"strings"
)

// supportedFieldRefTags are the baked in validations referencing a sibling
// (same level) field whose reverse dependencies StructPartialDependent knows
// how to track. The param of these tags is the referenced Go field name.
var supportedFieldRefTags = map[string]struct{}{
	"eqfield":  {},
	"nefield":  {},
	"gtfield":  {},
	"gtefield": {},
	"ltfield":  {},
	"ltefield": {},
}

// unsupportedFieldRefTags are baked in validations which also reference other
// fields, but whose dependencies StructPartialDependent does not attempt to
// resolve. Encountering any of them requires falling back to full validation
// so no dependent check is silently skipped.
var unsupportedFieldRefTags = map[string]struct{}{
	"eqcsfield":                     {},
	"necsfield":                     {},
	"gtcsfield":                     {},
	"gtecsfield":                    {},
	"ltcsfield":                     {},
	"ltecsfield":                    {},
	"fieldcontains":                 {},
	"fieldexcludes":                 {},
	"postcode_iso3166_alpha2_field": {},
	requiredIfTag:                   {},
	requiredUnlessTag:               {},
	requiredWithTag:                 {},
	requiredWithAllTag:              {},
	requiredWithoutTag:              {},
	requiredWithoutAllTag:           {},
	excludedIfTag:                   {},
	excludedUnlessTag:               {},
	excludedWithTag:                 {},
	excludedWithAllTag:              {},
	excludedWithoutTag:              {},
	excludedWithoutAllTag:           {},
	skipUnlessTag:                   {},
}

// PartialValidationResult reports the outcome of StructPartialDependent.
type PartialValidationResult struct {
	// RequiresFullValidation is true when the impact of the changed fields
	// could not be determined statically, eg. the struct registers a custom
	// struct level validation, uses cross level field references or an
	// unsupported field referencing tag, or a changed path cannot be resolved.
	// No validation was performed in that case and the caller must run a full
	// Struct() validation instead; nothing is silently skipped.
	RequiresFullValidation bool

	// ValidatedFields lists the changed and dependency affected fields that
	// were validated, as Go field paths relative to the provided struct
	// eg. "Start" or "Nested.Start". A path denoting a struct means the whole
	// nested struct was validated.
	ValidatedFields []string
}

// StructPartialDependent validates only the given changed fields plus every
// field whose validation outcome may depend on them, and reports the fields
// it validated.
//
// Fields are provided as Go field paths relative to the struct eg. "Start"
// or "Nested.Start". Reverse dependencies are derived from the baked in
// eqfield, nefield, gtfield, gtefield, ltfield and ltefield tags referencing
// sibling fields; regular field rules (including custom field level
// validations) run for the affected fields only, so unrelated validations
// are not executed. Every affected field is validated at most once, even
// when dependencies form a cycle.
//
// When the affected set cannot be determined safely (custom struct level
// validations, cross level or otherwise unsupported field references,
// unknown paths) the returned PartialValidationResult has
// RequiresFullValidation set to true, no validation is performed and the
// caller must fall back to Struct().
//
// It returns InvalidValidationError for bad values passed in and nil or
// ValidationErrors as error otherwise, exactly like Struct().
func (v *Validate) StructPartialDependent(s interface{}, fields ...string) (PartialValidationResult, error) {
	return v.StructPartialDependentCtx(context.Background(), s, fields...)
}

// StructPartialDependentCtx is the same as StructPartialDependent and also
// allows passing of contextual validation information via context.Context.
func (v *Validate) StructPartialDependentCtx(ctx context.Context, s interface{}, fields ...string) (res PartialValidationResult, err error) {
	val := reflect.ValueOf(s)
	top := val

	if val.Kind() == reflect.Ptr && !val.IsNil() {
		val = val.Elem()
	}

	if val.Kind() != reflect.Struct || val.Type().ConvertibleTo(timeType) {
		return res, &InvalidValidationError{Type: reflect.TypeOf(s)}
	}

	ns := ""
	if name := val.Type().Name(); len(name) != 0 {
		ns = name + namespaceSeparator
	}

	exact := make(map[string]struct{})
	var subtrees []string

	for _, f := range fields {
		if len(f) == 0 {
			continue
		}
		if !v.planPartialValidation(val.Type(), ns, strings.Split(f, namespaceSeparator), exact, &subtrees) {
			return PartialValidationResult{RequiresFullValidation: true}, nil
		}
	}

	// parents holds every intermediate namespace of the planned fields so the
	// traversal can descend into nested structs on the way to them.
	parents := make(map[string]struct{})
	addParents := func(path string) {
		for i := strings.Index(path, namespaceSeparator); i != -1; {
			parents[path[:i]] = struct{}{}
			if j := strings.Index(path[i+1:], namespaceSeparator); j != -1 {
				i += 1 + j
			} else {
				break
			}
		}
	}
	for path := range exact {
		addParents(path)
	}
	for _, path := range subtrees {
		addParents(path)
	}

	validated := make([]string, 0, len(exact)+len(subtrees))
	for path := range exact {
		validated = append(validated, strings.TrimPrefix(path, ns))
	}
	for _, path := range subtrees {
		validated = append(validated, strings.TrimPrefix(path, ns))
	}
	sort.Strings(validated)

	vd := v.pool.Get().(*validate)
	vd.top = top
	vd.isPartial = true
	vd.ffn = func(ns []byte) bool {
		s := string(ns)
		if _, ok := exact[s]; ok {
			return false
		}
		if _, ok := parents[s]; ok {
			return false
		}
		for _, sub := range subtrees {
			if s == sub || strings.HasPrefix(s, sub+namespaceSeparator) {
				return false
			}
		}
		return true
	}

	vd.validateStruct(ctx, top, val, val.Type(), vd.ns[0:0], vd.actualNs[0:0], nil)

	if len(vd.errs) > 0 {
		err = vd.errs
		vd.errs = nil
	}
	vd.ffn = nil

	v.pool.Put(vd)

	res.ValidatedFields = validated
	return res, err
}

// planPartialValidation resolves one changed field path against the struct
// type typ, records the namespaces that must be validated into exact (single
// fields) and subtrees (fully changed nested structs) and reports whether the
// affected set could be determined at all. ns is the namespace prefix of typ
// including a trailing separator, eg. "Struct." or "Struct.Nested.".
func (v *Validate) planPartialValidation(typ reflect.Type, ns string, segs []string, exact map[string]struct{}, subtrees *[]string) bool {
	cs, ok := v.structCache.Get(typ)
	if !ok {
		cs = v.extractStructCache(reflect.New(typ).Elem(), typ.Name())
	}

	// a custom struct level validation may inspect any field, so its
	// dependencies cannot be determined statically.
	if cs.fn != nil {
		return false
	}

	fieldsByName := make(map[string]*cField, len(cs.fields))
	deps := make(map[string][]string)

	for _, f := range cs.fields {
		fieldsByName[f.name] = f
		refs, ok := partialFieldRefs(f.cTags)
		if !ok {
			return false
		}
		for _, ref := range refs {
			deps[ref] = append(deps[ref], f.name)
		}
	}

	cf, ok := fieldsByName[segs[0]]
	if !ok {
		// unknown path, eg. a typo or an unexported/skipped field.
		return false
	}

	// breadth first walk of the reverse dependencies; the affected set
	// doubles as the visited set so dependency cycles terminate and every
	// field is collected at most once.
	affected := map[string]struct{}{segs[0]: {}}
	queue := []string{segs[0]}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, dep := range deps[cur] {
			if _, seen := affected[dep]; !seen {
				affected[dep] = struct{}{}
				queue = append(queue, dep)
			}
		}
	}

	isLeaf := len(segs) == 1
	for name := range affected {
		if !isLeaf && name == segs[0] {
			// path prefix field, descended into below; its own rules run
			// while traversing to the changed leaf.
			continue
		}
		f := fieldsByName[name]
		ftyp := typ.Field(f.idx).Type
		for ftyp.Kind() == reflect.Ptr {
			ftyp = ftyp.Elem()
		}
		if isLeaf && name == segs[0] && ftyp.Kind() == reflect.Struct && !ftyp.ConvertibleTo(timeType) {
			// the whole nested struct changed, validate it entirely,
			// including any struct level validation it may have.
			*subtrees = append(*subtrees, ns+name)
			continue
		}
		exact[ns+name] = struct{}{}
	}

	if isLeaf {
		return true
	}

	ftyp := typ.Field(cf.idx).Type
	for ftyp.Kind() == reflect.Ptr {
		ftyp = ftyp.Elem()
	}
	if ftyp.Kind() != reflect.Struct || ftyp.ConvertibleTo(timeType) {
		// cannot navigate into a non struct field.
		return false
	}
	return v.planPartialValidation(ftyp, ns+segs[0]+namespaceSeparator, segs[1:], exact, subtrees)
}

// partialFieldRefs walks a parsed tag chain and returns the names of the
// sibling fields the field depends on via the supported field comparison
// tags. ok is false when a tag is encountered whose dependencies cannot be
// determined, eg. a cross level reference or an unsupported field
// referencing tag.
func partialFieldRefs(ct *cTag) (refs []string, ok bool) {
	for ; ct != nil; ct = ct.next {
		if ct.keys != nil {
			krefs, kok := partialFieldRefs(ct.keys)
			if !kok {
				return nil, false
			}
			refs = append(refs, krefs...)
		}
		if !ct.hasTag {
			continue
		}
		if _, supported := supportedFieldRefTags[ct.tag]; supported {
			if len(ct.param) == 0 || strings.ContainsAny(ct.param, namespaceSeparator+leftBracket) {
				// cross level reference, cannot be resolved statically.
				return nil, false
			}
			refs = append(refs, ct.param)
			continue
		}
		if _, unsupported := unsupportedFieldRefTags[ct.tag]; unsupported {
			return nil, false
		}
	}
	return refs, true
}
