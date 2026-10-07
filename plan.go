package validator

import (
	"context"
	"fmt"
	"reflect"
	"strconv"
	"strings"
)

// SchemaErrorKind classifies the problems CompileSchema can report.
type SchemaErrorKind uint8

const (
	// SchemaErrUnknownTag indicates a validation tag with no registered
	// validation function and no registered alias.
	SchemaErrUnknownTag SchemaErrorKind = iota
	// SchemaErrAliasCycle indicates an alias whose expansion, directly or
	// transitively, refers back to itself.
	SchemaErrAliasCycle
	// SchemaErrInvalidParam indicates a malformed, missing or unparseable
	// tag parameter, eg. min=abc on a numeric field.
	SchemaErrInvalidParam
	// SchemaErrFieldRef indicates a cross-field tag parameter (eqfield,
	// eqcsfield, required_with, ...) referencing a field path that does not
	// exist on the (cross-)struct being compiled.
	SchemaErrFieldRef
	// SchemaErrDive indicates a dive/keys/endkeys structure problem, eg.
	// dive on a field that is not a slice, array or map.
	SchemaErrDive
	// SchemaErrInvalidTag indicates a structurally invalid tag, eg. an
	// empty tag produced by a trailing comma.
	SchemaErrInvalidTag
)

// SchemaError is a single compile-time diagnostic produced by CompileSchema.
// Path is the dot-separated struct field path (rooted at the compiled type
// name) of the offending field, eg. "Config.Server.Port".
type SchemaError struct {
	Path  string
	Tag   string
	Param string
	Kind  SchemaErrorKind
	msg   string
}

// Error returns the human readable diagnostic message.
func (e *SchemaError) Error() string {
	return e.msg
}

// SchemaErrors is the aggregate of all diagnostics found while compiling a
// schema. CompileSchema reports every problem it finds in a single pass
// instead of failing (or panicking) on the first one.
type SchemaErrors []*SchemaError

// Error implements the error interface.
func (se SchemaErrors) Error() string {
	b := make([]byte, 0, 64*len(se))
	for i, e := range se {
		if i > 0 {
			b = append(b, '\n')
		}
		b = append(b, e.msg...)
	}
	return string(b)
}

// InvalidPlanError is returned by StructWithPlan/StructWithPlanCtx when the
// supplied plan cannot be used with this *Validate instance, eg. because the
// validation registry changed after the plan was compiled.
type InvalidPlanError struct {
	Reason string
}

// Error implements the error interface.
func (e *InvalidPlanError) Error() string {
	return "validator: invalid plan: " + e.Reason
}

// Plan is an immutable, pre-compiled validation schema for a single struct
// type, produced by CompileSchema. A Plan guarantees that, at compile time:
//
//   - every validation tag on every (recursively reachable) field resolved
//     to a registered validation function or alias,
//   - alias expansion contained no cycles,
//   - parameters of the built-in tags were well-formed,
//   - every cross-field reference (eqfield, eqcsfield, required_with, ...)
//     resolved to an existing field path,
//
// without invoking any validation function or inspecting any value.
//
// A Plan is bound to the *Validate instance and the registry version it was
// compiled with: registering validations, aliases, struct level validations,
// map rules, custom type funcs, tag name funcs or changing the tag name
// invalidates previously compiled plans, and StructWithPlan will report an
// *InvalidPlanError for them.
type Plan struct {
	v       *Validate
	version uint64
	typ     reflect.Type
	fields  int
}

// Type returns the struct type the plan was compiled for.
func (p *Plan) Type() reflect.Type {
	return p.typ
}

// Fields returns the number of fields inspected while compiling the plan.
func (p *Plan) Fields() int {
	return p.fields
}

// crossFieldTags take a single parameter referencing a field of the
// immediately surrounding struct.
var crossFieldTags = map[string]struct{}{
	"eqfield": {}, "nefield": {}, "gtfield": {}, "gtefield": {}, "ltfield": {}, "ltefield": {},
	"fieldcontains": {}, "fieldexcludes": {},
}

// crossStructFieldTags take a single parameter referencing a field of the
// top-level struct being validated.
var crossStructFieldTags = map[string]struct{}{
	"eqcsfield": {}, "necsfield": {}, "gtcsfield": {}, "gtecsfield": {}, "ltcsfield": {}, "ltecsfield": {},
}

// fieldNameListTags take a space separated list of field names of the
// surrounding struct.
var fieldNameListTags = map[string]struct{}{
	requiredWithTag: {}, requiredWithAllTag: {}, requiredWithoutTag: {}, requiredWithoutAllTag: {},
	excludedWithTag: {}, excludedWithAllTag: {}, excludedWithoutTag: {}, excludedWithoutAllTag: {},
}

// fieldValuePairTags take space separated field=value pairs.
var fieldValuePairTags = map[string]struct{}{
	requiredIfTag: {}, requiredUnlessTag: {}, skipUnlessTag: {}, excludedIfTag: {}, excludedUnlessTag: {},
}

// numericParamTags require a parameter parseable as a number for every
// field kind they apply to.
var numericParamTags = map[string]struct{}{
	"len": {}, "min": {}, "max": {}, "gt": {}, "gte": {}, "lt": {}, "lte": {},
}

// maxAliasDepth guards alias expansion against pathological inputs even
// before cycle detection kicks in.
const maxAliasDepth = 64

type schemaCompiler struct {
	v        *Validate
	top      reflect.Type
	compiled map[reflect.Type]struct{}
	errs     SchemaErrors
	fields   int
}

// CompileSchema pre-compiles the validation schema for the given struct
// type: it parses every field tag, expands aliases and resolves cross-field
// references for the type and every type reachable from it, terminating on
// recursive types by type identity.
//
// It never invokes a validation function and never inspects a value; only
// type information and the registered tags/aliases are used.
//
// On success it returns an immutable *Plan usable with StructWithPlan. On
// failure it returns a nil plan and a SchemaErrors value listing every
// problem found (unknown tags, alias cycles, invalid parameters, dangling
// cross-field references, malformed dive usage, ...).
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
		v:        v,
		top:      typ,
		compiled: make(map[reflect.Type]struct{}),
	}
	c.compileStruct(typ, typ.Name())

	if len(c.errs) > 0 {
		return nil, c.errs
	}

	return &Plan{v: v, version: v.registryVersion, typ: typ, fields: c.fields}, nil
}

// StructWithPlan validates s exactly like Struct, but requires a *Plan
// previously obtained from CompileSchema for the type of s. The plan must
// have been compiled by the same *Validate instance and the validation
// registry must not have changed since; otherwise an *InvalidPlanError is
// returned. Validation itself (and the errors it returns) is identical to
// Struct.
func (v *Validate) StructWithPlan(s interface{}, plan *Plan) error {
	return v.StructWithPlanCtx(context.Background(), s, plan)
}

// StructWithPlanCtx is the same as StructWithPlan but allows passing of a
// context.Context for contextual validation information.
func (v *Validate) StructWithPlanCtx(ctx context.Context, s interface{}, plan *Plan) error {
	if plan == nil {
		return &InvalidPlanError{Reason: "plan is nil"}
	}
	if plan.v != v {
		return &InvalidPlanError{Reason: "plan was compiled by a different *Validate instance"}
	}
	if plan.version != v.registryVersion {
		return &InvalidPlanError{Reason: "validation registry changed after the plan was compiled; recompile with CompileSchema"}
	}

	val := reflect.ValueOf(s)
	if val.Kind() == reflect.Ptr && !val.IsNil() {
		val = val.Elem()
	}
	if val.Kind() != reflect.Struct || val.Type() != plan.typ {
		return &InvalidValidationError{Type: reflect.TypeOf(s)}
	}

	return v.StructCtx(ctx, s)
}

// compileStruct compiles every field of typ. Recursion (including recursive
// pointer types) is terminated by type identity: each struct type is
// compiled at most once per CompileSchema call.
func (c *schemaCompiler) compileStruct(typ reflect.Type, path string) {
	if _, ok := c.compiled[typ]; ok {
		return
	}
	c.compiled[typ] = struct{}{}

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

		c.fields++
		fieldPath := fld.Name
		if len(path) > 0 {
			fieldPath = path + namespaceSeparator + fld.Name
		}

		dive, elem := c.checkTagString(tag, fieldPath, fld.Type, typ, nil)

		// recurse into nested types by identity
		if dive {
			et := derefType(elem)
			if et != nil && et.Kind() == reflect.Struct && !et.ConvertibleTo(timeType) && !c.hasCustomFunc(et) {
				c.compileStruct(et, fieldPath+"[]")
			}
			continue
		}

		bt := derefType(fld.Type)
		if bt != nil && bt.Kind() == reflect.Struct && !bt.ConvertibleTo(timeType) && !c.hasCustomFunc(bt) {
			c.compileStruct(bt, fieldPath)
		}
	}
}

func (c *schemaCompiler) hasCustomFunc(typ reflect.Type) bool {
	if !c.v.hasCustomFuncs {
		return false
	}
	_, ok := c.v.customFuncs[typ]
	return ok
}

func derefType(t reflect.Type) reflect.Type {
	for t != nil && t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	return t
}

// checkTagString validates a full tag string against the given field type.
// It reports whether a dive tag was encountered and, if so, the element
// type the remaining tags apply to.
func (c *schemaCompiler) checkTagString(tag, path string, typ reflect.Type, structTyp reflect.Type, aliasStack []string) (dive bool, elem reflect.Type) {
	if len(tag) == 0 {
		return false, nil
	}
	return c.checkTokens(strings.Split(tag, tagSeparator), path, typ, structTyp, aliasStack)
}

func (c *schemaCompiler) checkTokens(tokens []string, path string, typ reflect.Type, structTyp reflect.Type, aliasStack []string) (dive bool, elem reflect.Type) {
	prevWasDive := false
	inKeys := false
	var container reflect.Type // slice/array/map type the current dive operates on

	for i := 0; i < len(tokens); i++ {
		t := tokens[i]

		// alias expansion happens at the token level, with cycle detection
		if expanded, ok := c.v.aliases[t]; ok {
			if c.aliasCycle(t, path, aliasStack) {
				continue
			}
			d, e := c.checkTagString(expanded, path, typ, structTyp, append(aliasStack, t))
			if d {
				dive, elem = d, e
				prevWasDive = true
			} else {
				prevWasDive = false
			}
			continue
		}

		switch t {
		case omitzero, omitempty, omitnil, structOnlyTag, noStructLevelTag, isdefault:
			prevWasDive = false
			continue

		case diveTag:
			base := derefType(typ)
			if base == nil {
				c.addErr(path, t, "", SchemaErrDive, fmt.Sprintf("'%s' tag on field '%s' cannot be resolved to a slice, array or map", diveTag, path))
				return dive, elem
			}
			switch base.Kind() {
			case reflect.Slice, reflect.Array, reflect.Map:
				dive, elem = true, base.Elem()
				container = base
			default:
				c.addErr(path, t, "", SchemaErrDive, fmt.Sprintf("'%s' tag on field '%s' requires a slice, array or map, found %s", diveTag, path, base.Kind()))
				return dive, elem
			}
			typ = elem
			prevWasDive = true
			continue

		case keysTag:
			if !prevWasDive || container == nil || container.Kind() != reflect.Map {
				c.addErr(path, t, "", SchemaErrDive, fmt.Sprintf("'%s' tag on field '%s' must immediately follow '%s' on a map field", keysTag, path, diveTag))
			}
			inKeys = true
			if container != nil && container.Kind() == reflect.Map {
				typ = container.Key()
			}
			prevWasDive = false
			continue

		case endKeysTag:
			if !inKeys {
				c.addErr(path, t, "", SchemaErrDive, fmt.Sprintf("'%s' tag on field '%s' encountered without a corresponding '%s' tag", endKeysTag, path, keysTag))
			}
			inKeys = false
			// restore element type for validations after endkeys
			typ = elem
			prevWasDive = false
			continue
		}

		prevWasDive = false

		// regular validation tag(s), possibly or-separated
		for _, orVal := range strings.Split(t, orSeparator) {
			vals := strings.SplitN(orVal, tagKeySeparator, 2)
			name := vals[0]
			param := ""
			hasParam := len(vals) > 1
			if hasParam {
				param = strings.ReplaceAll(strings.ReplaceAll(vals[1], utf8HexComma, ","), utf8Pipe, "|")
			}
			c.checkOne(name, param, hasParam, path, typ, structTyp, aliasStack)
		}
	}

	if inKeys {
		c.addErr(path, keysTag, "", SchemaErrDive, fmt.Sprintf("'%s' tag on field '%s' is missing a closing '%s' tag", keysTag, path, endKeysTag))
	}

	return dive, elem
}

func (c *schemaCompiler) aliasCycle(alias, path string, stack []string) bool {
	if len(stack) >= maxAliasDepth {
		c.addErr(path, alias, "", SchemaErrAliasCycle, fmt.Sprintf("alias '%s' on field '%s' exceeds the maximum expansion depth (possible cycle)", alias, path))
		return true
	}
	for _, a := range stack {
		if a == alias {
			c.addErr(path, alias, "", SchemaErrAliasCycle, fmt.Sprintf("alias cycle detected expanding '%s' on field '%s'", alias, path))
			return true
		}
	}
	return false
}

// checkOne validates a single tag name + parameter combination.
func (c *schemaCompiler) checkOne(name, param string, hasParam bool, path string, typ reflect.Type, structTyp reflect.Type, aliasStack []string) {
	if len(name) == 0 {
		c.addErr(path, name, param, SchemaErrInvalidTag, fmt.Sprintf("invalid empty validation tag on field '%s'", path))
		return
	}

	if _, ok := c.v.validations[name]; ok {
		c.checkParam(name, param, hasParam, path, typ, structTyp)
		return
	}

	// a tag may itself be an alias (eg. inside an or-expression)
	if expanded, ok := c.v.aliases[name]; ok {
		if c.aliasCycle(name, path, aliasStack) {
			return
		}
		c.checkTagString(expanded, path, typ, structTyp, append(aliasStack, name))
		return
	}

	c.addErr(path, name, param, SchemaErrUnknownTag, fmt.Sprintf("undefined validation function '%s' on field '%s'", name, path))
}

// checkParam statically validates the parameter of known built-in tags.
// Parameters of user-registered validations are not inspected.
func (c *schemaCompiler) checkParam(name, param string, hasParam bool, path string, typ reflect.Type, structTyp reflect.Type) {
	if _, ok := crossFieldTags[name]; ok {
		if c.requireParam(name, param, hasParam, path) {
			c.checkFieldRef(name, param, path, structTyp)
		}
		return
	}

	if _, ok := crossStructFieldTags[name]; ok {
		if c.requireParam(name, param, hasParam, path) {
			c.checkFieldRef(name, param, path, c.top)
		}
		return
	}

	if _, ok := fieldNameListTags[name]; ok {
		if !c.requireParam(name, param, hasParam, path) {
			return
		}
		for _, f := range strings.Fields(param) {
			c.checkFieldRef(name, f, path, structTyp)
		}
		return
	}

	if _, ok := fieldValuePairTags[name]; ok {
		if !c.requireParam(name, param, hasParam, path) {
			return
		}
		params := strings.Fields(param)
		if len(params)%2 != 0 {
			c.addErr(path, name, param, SchemaErrInvalidParam, fmt.Sprintf("bad param number for '%s' on field '%s': parameters must be field/value pairs", name, path))
			return
		}
		for i := 0; i < len(params); i += 2 {
			c.checkFieldRef(name, params[i], path, structTyp)
		}
		return
	}

	if _, ok := numericParamTags[name]; ok {
		if !c.requireParam(name, param, hasParam, path) {
			return
		}
		// time.Time fields accept params handled specially at runtime
		if base := derefType(typ); base != nil && base.ConvertibleTo(timeType) {
			return
		}
		if _, err := strconv.ParseFloat(param, 64); err != nil {
			c.addErr(path, name, param, SchemaErrInvalidParam, fmt.Sprintf("invalid parameter '%s' for '%s' on field '%s': must be a number", param, name, path))
		}
		return
	}
}

func (c *schemaCompiler) requireParam(name, param string, hasParam bool, path string) bool {
	if !hasParam || len(param) == 0 {
		c.addErr(path, name, param, SchemaErrInvalidParam, fmt.Sprintf("missing parameter for '%s' on field '%s'", name, path))
		return false
	}
	return true
}

// checkFieldRef verifies that a dot-separated field reference resolves
// against root.
func (c *schemaCompiler) checkFieldRef(tag, ref, path string, root reflect.Type) {
	if len(ref) == 0 {
		return
	}
	if resolveFieldPath(root, ref) {
		return
	}
	rootName := ""
	if root != nil {
		rootName = root.Name()
	}
	c.addErr(path, tag, ref, SchemaErrFieldRef, fmt.Sprintf("cross-field reference '%s' for tag '%s' on field '%s' does not resolve to a field of '%s'", ref, tag, path, rootName))
}

// resolveFieldPath walks a dot-separated field path (eg. "Inner.Field")
// starting at root, dereferencing pointers along the way.
func resolveFieldPath(root reflect.Type, ref string) bool {
	cur := root
	for _, seg := range strings.Split(ref, namespaceSeparator) {
		cur = derefType(cur)
		if cur == nil || cur.Kind() != reflect.Struct {
			return false
		}
		fld, ok := cur.FieldByName(seg)
		if !ok {
			return false
		}
		cur = fld.Type
	}
	return true
}

func (c *schemaCompiler) addErr(path, tag, param string, kind SchemaErrorKind, msg string) {
	c.errs = append(c.errs, &SchemaError{
		Path:  path,
		Tag:   tag,
		Param: param,
		Kind:  kind,
		msg:   msg,
	})
}
