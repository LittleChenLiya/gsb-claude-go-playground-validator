package validator

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// PlanError describes a single problem discovered while pre-compiling
// the validation schema of a type via CompileSchema.
//
// All problems found across the whole type graph (unknown tags, alias
// cycles, invalid parameters and unresolvable cross-field references) are
// aggregated and reported at once; no validation function is ever invoked
// and no business value is ever read while compiling.
type PlanError struct {
	// Path is the dotted struct field path where the problem was found,
	// e.g. "Outer.Inner.Name".
	Path string

	// Tag is the offending validation tag, when applicable.
	Tag string

	// Param is the offending tag parameter, when applicable.
	Param string

	// Message is a human readable description of the problem.
	Message string
}

// Error implements the error interface for a single PlanError.
func (d PlanError) Error() string {
	if d.Tag == "" {
		return d.Path + ": " + d.Message
	}
	if d.Param == "" {
		return fmt.Sprintf("%s: tag '%s': %s", d.Path, d.Tag, d.Message)
	}
	return fmt.Sprintf("%s: tag '%s' param '%s': %s", d.Path, d.Tag, d.Param, d.Message)
}

// PlanErrors is the aggregated set of problems reported by
// CompileSchema. It implements the error interface.
type PlanErrors []PlanError

// Error implements the error interface, summarizing every diagnostic.
func (ds PlanErrors) Error() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "validator: schema compilation failed with %d problem(s):", len(ds))
	for _, d := range ds {
		sb.WriteString("\n  - ")
		sb.WriteString(d.Error())
	}
	return sb.String()
}

// StalePlanError is returned by StructWithPlan and StructWithPlanCtx when
// the validation registry of the *Validate instance (registered validations,
// aliases, struct level validations, custom type funcs, tag name settings)
// changed after the Plan was compiled. The plan must be recompiled with
// CompileSchema before it can be used again.
type StalePlanError struct {
	// CompiledRegistryVersion is the registry version the plan was compiled against.
	CompiledRegistryVersion uint64

	// CurrentRegistryVersion is the registry version observed when the plan was used.
	CurrentRegistryVersion uint64
}

// Error implements the error interface.
func (e *StalePlanError) Error() string {
	return fmt.Sprintf("validator: validation plan is stale (compiled against registry version %d, current version is %d); recompile with CompileSchema",
		e.CompiledRegistryVersion, e.CurrentRegistryVersion)
}

var (
	// ErrNilPlan is returned by StructWithPlan and StructWithPlanCtx when a nil plan is supplied.
	ErrNilPlan = errors.New("validator: nil *Plan supplied")

	// ErrPlanValidatorMismatch is returned by StructWithPlan and StructWithPlanCtx
	// when the plan was compiled by a different *Validate instance.
	ErrPlanValidatorMismatch = errors.New("validator: plan was compiled by a different *Validate instance")
)

// Plan is an immutable, precompiled validation schema for a single struct
// type, produced by CompileSchema. A Plan captures the parsed tags, expanded
// aliases and resolved cross-field references of every struct type reachable
// from the root type at compile time and can be executed any number of times
// via StructWithPlan as long as the *Validate registry does not change.
//
// A Plan is safe for concurrent use by multiple goroutines.
type Plan struct {
	v        *Validate
	typ      reflect.Type
	registry uint64
	structs  map[reflect.Type]*cStruct
}

// Type returns the root struct type the plan was compiled for.
func (p *Plan) Type() reflect.Type {
	return p.typ
}

// RegistryVersion returns the version of the *Validate registry the plan was
// compiled against. When the registry of the owning *Validate instance
// changes (e.g. a new validation or alias is registered) the plan becomes
// stale and StructWithPlan reports a *StalePlanError.
func (p *Plan) RegistryVersion() uint64 {
	return p.registry
}

// CompileSchema pre-compiles the validation schema for the given struct type
// (a pointer to a struct type is also accepted and dereferenced).
//
// It parses every validation tag of every struct type reachable from typ
// ahead of time - expanding aliases, checking parameters and resolving
// cross-field references such as 'eqfield' - and returns an immutable *Plan
// that can be executed later with StructWithPlan. Recursive types terminate
// by type identity, so self-referential types are supported.
//
// Compilation never invokes any validation function (custom or baked-in) and
// never reads any business object value; it is a pure type-level analysis.
//
// When any problem is found - unknown tags, alias cycles, invalid parameters
// or cross-field references that do not exist - compilation fails and all
// problems are returned at once as a PlanErrors error; no plan is
// returned in that case.
func (v *Validate) CompileSchema(typ reflect.Type) (*Plan, error) {
	if typ == nil {
		return nil, &InvalidValidationError{Type: nil}
	}

	for typ.Kind() == reflect.Ptr {
		typ = typ.Elem()
	}

	if typ.Kind() != reflect.Struct || typ.ConvertibleTo(timeType) {
		return nil, &InvalidValidationError{Type: typ}
	}

	c := &schemaCompiler{
		v:       v,
		root:    typ,
		visited: make(map[reflect.Type]struct{}),
		structs: make(map[reflect.Type]*cStruct),
	}
	c.compileStruct(typ, typ.Name())

	if len(c.diags) > 0 {
		return nil, c.diags
	}

	return &Plan{
		v:        v,
		typ:      typ,
		registry: atomic.LoadUint64(&v.registryVersion),
		structs:  c.structs,
	}, nil
}

// StructWithPlan validates s using a previously compiled Plan, reusing the
// exact parsed tags captured by CompileSchema instead of the shared caches.
//
// s must be of the same struct type the plan was compiled for (a pointer to
// it is also accepted), otherwise an InvalidValidationError is returned.
//
// If the registry of this *Validate instance changed after the plan was
// compiled (e.g. RegisterValidation, RegisterAlias, RegisterStructValidation,
// RegisterCustomTypeFunc, SetTagName or RegisterTagNameFunc was called) a
// *StalePlanError is returned and the plan must be recompiled. The regular
// Struct validation path is unaffected by plans and keeps working either way.
//
// It returns nil or ValidationErrors as error otherwise.
// You will need to assert the error if it's not nil eg. err.(validator.ValidationErrors) to access the array of errors.
func (v *Validate) StructWithPlan(s interface{}, plan *Plan) error {
	return v.StructWithPlanCtx(context.Background(), s, plan)
}

// StructWithPlanCtx does the same as StructWithPlan and allows passing of
// contextual validation information via context.Context.
func (v *Validate) StructWithPlanCtx(ctx context.Context, s interface{}, plan *Plan) (err error) {
	if plan == nil {
		return ErrNilPlan
	}

	if plan.v != v {
		return ErrPlanValidatorMismatch
	}

	if current := atomic.LoadUint64(&v.registryVersion); current != plan.registry {
		return &StalePlanError{CompiledRegistryVersion: plan.registry, CurrentRegistryVersion: current}
	}

	val := reflect.ValueOf(s)
	top := val

	if val.Kind() == reflect.Ptr && !val.IsNil() {
		val = val.Elem()
	}

	if val.Kind() != reflect.Struct || val.Type() != plan.typ {
		return &InvalidValidationError{Type: reflect.TypeOf(s)}
	}

	// good to validate
	vd := v.pool.Get().(*validate)
	vd.top = top
	vd.isPartial = false
	vd.plan = plan

	vd.validateStruct(ctx, top, val, val.Type(), vd.ns[0:0], vd.actualNs[0:0], nil)

	if len(vd.errs) > 0 {
		err = vd.errs
		vd.errs = nil
	}

	vd.plan = nil
	v.pool.Put(vd)

	return
}

// schemaCompiler performs the static, type-level analysis backing CompileSchema.
type schemaCompiler struct {
	v       *Validate
	root    reflect.Type
	visited map[reflect.Type]struct{}
	structs map[reflect.Type]*cStruct
	diags   PlanErrors
}

// compileStruct statically checks every field tag of typ and records the
// resulting *cStruct. Recursive types terminate by type identity: each
// struct type is compiled exactly once no matter how often it is reachable.
func (c *schemaCompiler) compileStruct(typ reflect.Type, path string) {
	if _, ok := c.visited[typ]; ok {
		return
	}
	c.visited[typ] = struct{}{}

	cs := &cStruct{name: typ.Name(), fields: make([]*cField, 0), fn: c.v.structLevelFuncs[typ]}
	c.structs[typ] = cs

	rules := c.v.rules[typ]

	for i := 0; i < typ.NumField(); i++ {
		fld := typ.Field(i)

		if !c.v.privateFieldValidation && !fld.Anonymous && len(fld.PkgPath) > 0 {
			continue
		}

		var tag string
		if rtag, ok := rules[fld.Name]; ok {
			tag = rtag
		} else {
			tag = fld.Tag.Get(c.v.tagName)
		}

		if tag == skipValidationTag {
			continue
		}

		customName := fld.Name
		if c.v.hasTagNameFunc {
			name := c.v.tagNameFunc(fld)
			if len(name) > 0 || c.v.omitBlankFieldNames {
				customName = name
			}
		}

		fieldPath := fld.Name
		if path != "" {
			fieldPath = path + namespaceSeparator + fld.Name
		}

		var ctag *cTag
		if len(tag) > 0 {
			// static analysis first: aggregate every problem instead of
			// panicking on the first one like the runtime parser does.
			before := len(c.diags)
			c.checkFieldTags(tag, fld.Type, typ, fieldPath, nil)
			if len(c.diags) == before {
				ctag = c.parseFieldTag(tag, fld.Name, fieldPath)
			}
		} else {
			// even if field doesn't have validations need cTag for traversing to potential inner/nested
			// elements of the field.
			ctag = new(cTag)
		}

		cs.fields = append(cs.fields, &cField{
			idx:        i,
			name:       fld.Name,
			altName:    customName,
			cTags:      ctag,
			namesEqual: fld.Name == customName,
		})

		c.descend(fld.Type, tag, fieldPath)
	}
}

// parseFieldTag builds the runtime tag chain for an already statically
// checked tag. The recover is a safety net only: every problem the runtime
// parser can panic on has already been reported as a diagnostic.
func (c *schemaCompiler) parseFieldTag(tag, fieldName, fieldPath string) (ctag *cTag) {
	defer func() {
		if r := recover(); r != nil {
			c.diags = append(c.diags, PlanError{Path: fieldPath, Message: fmt.Sprintf("%v", r)})
			ctag = nil
		}
	}()
	ctag, _ = c.v.parseFieldTagsRecursive(tag, fieldName, "", false)
	return
}

// descend mirrors the runtime traversal: nested structs are always entered,
// slice/array/map elements only when the 'dive' tag is present.
func (c *schemaCompiler) descend(ft reflect.Type, tag string, fieldPath string) {
	t := derefType(ft)

	switch t.Kind() {
	case reflect.Struct:
		if !t.ConvertibleTo(timeType) {
			c.compileStruct(t, fieldPath)
		}
	case reflect.Slice, reflect.Array, reflect.Map:
		if c.v.tagExpandsToDive(tag) {
			et := derefType(t.Elem())
			if et.Kind() == reflect.Struct && !et.ConvertibleTo(timeType) {
				c.compileStruct(et, fieldPath)
			}
		}
	}
}

// tagExpandsToDive reports whether the tag, after alias expansion, contains
// the 'dive' tag. Cycles in aliases are tolerated here; they are reported by
// the static checker.
func (v *Validate) tagExpandsToDive(tag string) bool {
	seen := make(map[string]struct{})
	var scan func(tag string) bool
	scan = func(tag string) bool {
		for _, t := range strings.Split(tag, tagSeparator) {
			if t == diveTag {
				return true
			}
			if av, ok := v.aliases[t]; ok {
				if _, dup := seen[t]; dup {
					continue
				}
				seen[t] = struct{}{}
				if scan(av) {
					return true
				}
			}
		}
		return false
	}
	return scan(tag)
}

// checkFieldTags statically checks a comma separated tag list. target is the
// type the tags apply to (updated as 'dive' is encountered), owner is the
// struct type containing the field (used to resolve same-struct cross-field
// references) and chain tracks the alias expansion stack for cycle
// detection. It reports whether the last processed token was 'dive'.
func (c *schemaCompiler) checkFieldTags(tag string, target reflect.Type, owner reflect.Type, path string, chain []string) (lastDive bool) {
	tags := strings.Split(tag, tagSeparator)

	var container reflect.Type // innermost slice/array/map entered via 'dive'

	for i := 0; i < len(tags); i++ {
		t := tags[i]

		// expand aliases, detecting cycles along the way
		if tagsVal, found := c.v.aliases[t]; found {
			if c.aliasCycle(path, t, chain) {
				continue
			}
			lastDive = c.checkFieldTags(tagsVal, target, owner, path, append(chain, t))
			continue
		}

		switch t {
		case diveTag:
			container = derefType(target)
			switch container.Kind() {
			case reflect.Slice, reflect.Array, reflect.Map:
				target = container.Elem()
			case reflect.Interface:
				// runtime dependent, be lenient
			default:
				c.addDiag(path, diveTag, "", fmt.Sprintf("dive error! can't dive on a non slice or map (field of kind '%s')", container.Kind()))
			}
			lastDive = true

		case keysTag:
			if !lastDive {
				c.addDiag(path, keysTag, "", fmt.Sprintf("'%s' tag must be immediately preceded by the '%s' tag", keysTag, diveTag))
			}

			b := make([]byte, 0, 64)
			found := false

			i++
			for ; i < len(tags); i++ {
				b = append(b, tags[i]...)
				b = append(b, ',')

				if tags[i] == endKeysTag {
					found = true
					break
				}
			}

			if !found {
				c.addDiag(path, keysTag, "", "'"+endKeysTag+"' tag not found for corresponding '"+keysTag+"' tag")
				return false
			}

			keyTarget := target
			if container != nil && container.Kind() == reflect.Map {
				keyTarget = container.Key()
			}
			c.checkFieldTags(string(b[:len(b)-1]), keyTarget, owner, path, nil)
			lastDive = false

		case endKeysTag:
			if i != len(tags)-1 {
				c.addDiag(path, endKeysTag, "", keysTagNotDefined)
			}
			return false

		case omitzero, omitempty, omitnil, structOnlyTag, noStructLevelTag, isdefault:
			lastDive = false

		default:
			c.checkValidationTag(t, target, owner, path, chain)
			lastDive = false
		}
	}
	return lastDive
}

// checkValidationTag checks a single validation expression (possibly an
// 'or' separated list) against the registry and the field/target types.
func (c *schemaCompiler) checkValidationTag(t string, target reflect.Type, owner reflect.Type, path string, chain []string) {
	orVals := strings.Split(t, orSeparator)

	for _, ov := range orVals {
		vals := strings.SplitN(ov, tagKeySeparator, 2)
		tagName := vals[0]

		if len(tagName) == 0 {
			c.addDiag(path, "", "", strings.TrimSpace(fmt.Sprintf(invalidValidation, path)))
			continue
		}

		var param string
		if len(vals) > 1 {
			param = strings.ReplaceAll(strings.ReplaceAll(vals[1], utf8HexComma, ","), utf8Pipe, "|")
		}

		if _, ok := c.v.validations[tagName]; ok {
			c.checkTagParam(tagName, param, target, owner, path)
			continue
		}

		if aliasTag, isAlias := c.v.aliases[tagName]; isAlias {
			if c.aliasCycle(path, tagName, chain) {
				continue
			}
			c.checkFieldTags(aliasTag, target, owner, path, append(chain, tagName))
			continue
		}

		c.addDiag(path, tagName, param, strings.TrimSpace(fmt.Sprintf(undefinedValidation, tagName, path)))
	}
}

// aliasCycle reports the alias as a cycle, recording a diagnostic, when it
// already appears in the current expansion chain.
func (c *schemaCompiler) aliasCycle(path, alias string, chain []string) bool {
	for _, a := range chain {
		if a == alias {
			c.addDiag(path, alias, "", fmt.Sprintf("alias cycle detected: %s -> %s", strings.Join(chain, " -> "), alias))
			return true
		}
	}
	return false
}

// checkTagParam validates the parameter of a known tag where it can be
// verified statically: cross-field references, field-name lists and numeric
// parameters.
func (c *schemaCompiler) checkTagParam(tag, param string, target reflect.Type, owner reflect.Type, path string) {
	switch tag {
	case "eqfield", "nefield", "gtfield", "gtefield", "ltfield", "ltefield", "fieldcontains", "fieldexcludes":
		c.checkCrossFieldParam(tag, param, owner, path)

	case "eqcsfield", "necsfield", "gtcsfield", "gtecsfield", "ltcsfield", "ltecsfield":
		c.checkCrossFieldParam(tag, param, c.root, path)

	case requiredIfTag, requiredUnlessTag, excludedIfTag, excludedUnlessTag, skipUnlessTag:
		params := parseOneOfParam2(param)
		if len(params)%2 != 0 {
			c.addDiag(path, tag, param, fmt.Sprintf("bad param number for %s", tag))
			return
		}
		for i := 0; i < len(params); i += 2 {
			c.checkFieldRef(tag, params[i], owner, path)
		}

	case requiredWithTag, requiredWithAllTag, requiredWithoutTag, requiredWithoutAllTag,
		excludedWithTag, excludedWithAllTag, excludedWithoutTag, excludedWithoutAllTag:
		for _, p := range parseOneOfParam2(param) {
			c.checkFieldRef(tag, p, owner, path)
		}

	case "unique":
		if param == "" {
			return
		}
		t := derefType(target)
		if t.Kind() != reflect.Slice && t.Kind() != reflect.Array {
			return
		}
		et := derefType(t.Elem())
		if et.Kind() == reflect.Struct && !et.ConvertibleTo(timeType) {
			c.checkFieldRef(tag, param, et, path)
		}

	case "len", "min", "max", "eq", "ne", "lt", "lte", "gt", "gte":
		c.checkNumericParam(tag, param, target, path)
	}
}

// checkCrossFieldParam verifies that a cross-field parameter is present and
// resolves against base.
func (c *schemaCompiler) checkCrossFieldParam(tag, param string, base reflect.Type, path string) {
	if param == "" {
		c.addDiag(path, tag, param, fmt.Sprintf("tag '%s' requires a field path parameter", tag))
		return
	}
	c.checkFieldRef(tag, param, base, path)
}

// checkFieldRef records a diagnostic when the dotted field path ref
// provably does not exist on base.
func (c *schemaCompiler) checkFieldRef(tag, ref string, base reflect.Type, path string) {
	if !resolveFieldPath(base, ref) {
		c.addDiag(path, tag, ref, fmt.Sprintf("cross-field reference '%s' not found in struct '%s'", ref, base.Name()))
	}
}

// checkNumericParam verifies that the parameter parses the same way the
// baked-in comparison validators will parse it at runtime for the target
// kind, turning runtime panics into compile time diagnostics.
func (c *schemaCompiler) checkNumericParam(tag, param string, target reflect.Type, path string) {
	t := derefType(target)

	var err error
	switch t.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if t == timeDurationType {
			if _, derr := time.ParseDuration(param); derr == nil {
				break
			}
		}
		_, err = strconv.ParseInt(param, 0, 64)

	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		_, err = strconv.ParseUint(param, 0, 64)

	case reflect.Float32, reflect.Float64:
		_, err = strconv.ParseFloat(param, 64)

	case reflect.Bool:
		if tag == "eq" || tag == "ne" {
			_, err = strconv.ParseBool(param)
		}

	case reflect.String:
		// eq/ne compare the string value itself, the others compare its length
		if tag != "eq" && tag != "ne" {
			_, err = strconv.ParseInt(param, 0, 64)
		}

	case reflect.Slice, reflect.Map, reflect.Array:
		_, err = strconv.ParseInt(param, 0, 64)
	}

	if err != nil {
		c.addDiag(path, tag, param, fmt.Sprintf("invalid parameter '%s' for tag '%s' on field of kind '%s'", param, tag, t.Kind()))
	}
}

// addDiag appends a diagnostic to the compilation result.
func (c *schemaCompiler) addDiag(path, tag, param, message string) {
	c.diags = append(c.diags, PlanError{Path: path, Tag: tag, Param: param, Message: message})
}

// derefType follows pointers to their element type.
func derefType(t reflect.Type) reflect.Type {
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	return t
}

// resolveFieldPath statically resolves a dotted cross-field path (the same
// shape accepted by GetStructFieldOKAdvanced) against typ.
//
// It only reports failure when a segment provably does not exist; dynamic
// positions (interfaces, map/slice indexing) are accepted leniently because
// they can only be verified against a live value.
func resolveFieldPath(typ reflect.Type, ns string) bool {
	for {
		typ = derefType(typ)

		if ns == "" {
			return true
		}

		switch typ.Kind() {
		case reflect.Struct:
			if typ.ConvertibleTo(timeType) {
				// time.Time has no validatable sub-fields
				return false
			}

			seg := ns
			rest := ""
			if idx := strings.Index(ns, namespaceSeparator); idx != -1 {
				seg = ns[:idx]
				rest = ns[idx+1:]
			}

			name := seg
			bracket := ""
			if bi := strings.Index(seg, leftBracket); bi != -1 {
				name = seg[:bi]
				bracket = seg[bi:]
			}

			sf, ok := typ.FieldByName(name)
			if !ok {
				return false
			}

			typ = sf.Type
			switch {
			case bracket != "" && rest != "":
				ns = bracket + namespaceSeparator + rest
			case bracket != "":
				ns = bracket
			default:
				ns = rest
			}

		case reflect.Slice, reflect.Array, reflect.Map:
			if !strings.HasPrefix(ns, leftBracket) {
				// not an indexable path; runtime dependent, be lenient
				return true
			}
			end := strings.Index(ns, rightBracket)
			if end == -1 {
				return true
			}
			typ = typ.Elem()
			ns = strings.TrimPrefix(ns[end+1:], namespaceSeparator)

		default:
			// interfaces and other dynamic kinds cannot be verified statically
			return true
		}
	}
}
