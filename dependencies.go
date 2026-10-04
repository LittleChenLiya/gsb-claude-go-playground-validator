package validator

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
)

// FieldDependency explicitly declares that the custom struct-level validation
// rule(s) associated with Field read the values of the fields listed in Reads.
//
// Field and every entry of Reads are field paths resolved using the same
// naming rules the validator applies when parsing a struct: each path segment
// may be either the Go struct field name or the alternate name produced by a
// registered TagNameFunc (see RegisterTagNameFunc), and nested struct fields
// are addressed with dot-separated segments, e.g. "Shipping.Address".
//
// The declarations are purely static metadata: they are resolved against the
// validator's parsed field information at registration time and are never
// derived from, nor cause the execution of, any user validation function.
type FieldDependency struct {
	// Field is the path of the field the rule is responsible for.
	Field string
	// Reads lists the paths of the other fields the rule reads.
	Reads []string
}

// ValidationPlan is the static execution plan derived from the field
// dependency declarations of a single struct type.
type ValidationPlan struct {
	// Groups lists the dependency-referenced field paths in dependency
	// order: a field only appears after every field it reads. Fields within
	// the same group are independent of one another and may be validated in
	// any order. The grouping is stable for a given struct definition and
	// set of declarations.
	Groups [][]string
}

// Contains reports whether the given canonical field path is part of the plan.
func (p *ValidationPlan) Contains(path string) bool {
	for _, group := range p.Groups {
		for _, g := range group {
			if g == path {
				return true
			}
		}
	}
	return false
}

// UnknownDependencyFieldError is returned at registration time when a field
// dependency declaration references a field path that does not exist on the
// struct, as resolved by the validator's field parsing and naming rules.
type UnknownDependencyFieldError struct {
	StructName string
	Path       string
}

func (e *UnknownDependencyFieldError) Error() string {
	return fmt.Sprintf("validator: field dependency declaration for struct '%s' references unknown field path '%s'", e.StructName, e.Path)
}

// DependencyCycleError is returned at registration time when the declared
// field dependencies form a cycle. Chain contains the concrete field paths
// forming the cycle, with the first element repeated at the end.
type DependencyCycleError struct {
	StructName string
	Chain      []string
}

func (e *DependencyCycleError) Error() string {
	return fmt.Sprintf("validator: field dependency cycle detected in struct '%s': %s", e.StructName, strings.Join(e.Chain, " -> "))
}

// RegisterStructValidationWithDeps registers a StructLevelFunc against a
// number of types, exactly like RegisterStructValidation, and additionally
// records the explicit cross-field dependency declarations for each type.
//
// The declarations are resolved statically at registration time using the
// validator's parsed field information (including TagNameFunc alternate
// names and nested struct paths); no user validation function is executed
// while building the graph. If a declaration references an unknown field an
// *UnknownDependencyFieldError is returned; this check runs before cycle
// detection. If the declared dependencies contain a cycle a
// *DependencyCycleError is returned, carrying the concrete field chain. On
// error nothing is registered.
//
// The resulting plan can be retrieved with StructValidationPlan. Types
// registered through the older RegisterStructValidation/...Ctx APIs simply
// have no plan, and their validation behaviour is unchanged.
//
// NOTE:
// - this method is not thread-safe it is intended that these all be registered prior to any validation
func (v *Validate) RegisterStructValidationWithDeps(fn StructLevelFunc, deps []FieldDependency, types ...interface{}) error {
	return v.RegisterStructValidationWithDepsCtx(wrapStructLevelFunc(fn), deps, types...)
}

// RegisterStructValidationWithDepsCtx registers a StructLevelFuncCtx against
// a number of types and allows passing of contextual validation information
// via context.Context, exactly like RegisterStructValidationCtx, and
// additionally records the explicit cross-field dependency declarations for
// each type. See RegisterStructValidationWithDeps for details.
//
// NOTE:
// - this method is not thread-safe it is intended that these all be registered prior to any validation
func (v *Validate) RegisterStructValidationWithDepsCtx(fn StructLevelFuncCtx, deps []FieldDependency, types ...interface{}) error {
	if v.structLevelFuncs == nil {
		v.structLevelFuncs = make(map[reflect.Type]StructLevelFuncCtx)
	}

	structTypes := make([]reflect.Type, 0, len(types))

	for _, t := range types {
		tv := reflect.ValueOf(t)
		if tv.Kind() == reflect.Ptr {
			t = reflect.Indirect(tv).Interface()
		}

		typ := reflect.TypeOf(t)
		if typ.Kind() != reflect.Struct {
			continue
		}

		structTypes = append(structTypes, typ)
	}

	// Register the function before building the plans: plan building parses
	// (and caches) the struct metadata, which captures the registered
	// struct-level function. Previous registrations are remembered so a
	// failed registration leaves no trace behind.
	prevFns := make(map[reflect.Type]StructLevelFuncCtx, len(structTypes))
	for _, typ := range structTypes {
		prevFns[typ] = v.structLevelFuncs[typ]
		v.structLevelFuncs[typ] = fn
	}

	plans := make(map[reflect.Type]*ValidationPlan, len(structTypes))

	for _, typ := range structTypes {
		plan, err := v.buildValidationPlan(typ, deps)
		if err != nil {
			// roll back everything, including any cached struct metadata
			for _, rt := range structTypes {
				if prevFns[rt] == nil {
					delete(v.structLevelFuncs, rt)
				} else {
					v.structLevelFuncs[rt] = prevFns[rt]
				}
				if cs, ok := v.structCache.Get(rt); ok {
					cs.fn = prevFns[rt]
				}
			}
			return err
		}

		plans[typ] = plan
	}

	if v.structValidationPlans == nil {
		v.structValidationPlans = make(map[reflect.Type]*ValidationPlan)
	}

	for _, typ := range structTypes {
		v.structValidationPlans[typ] = plans[typ]
	}

	return nil
}

// StructValidationPlan returns the static dependency plan computed at
// registration time for the given type by RegisterStructValidationWithDeps
// or RegisterStructValidationWithDepsCtx. The second return value is false
// when the type was registered without dependency declarations (or not
// registered at all).
func (v *Validate) StructValidationPlan(t interface{}) (*ValidationPlan, bool) {
	tv := reflect.ValueOf(t)
	if tv.Kind() == reflect.Ptr {
		t = reflect.Indirect(tv).Interface()
	}

	plan, ok := v.structValidationPlans[reflect.TypeOf(t)]
	return plan, ok
}

// depNode is a single node of the static field dependency graph.
type depNode struct {
	path  string   // canonical field path, using the validator's alternate names
	order int      // index of the top-level struct field, for stable ordering
	reads []string // canonical paths this node depends on
}

// buildValidationPlan resolves the declared dependencies against the parsed
// fields of typ and returns the resulting plan. Unknown field paths are
// reported before any cycle detection takes place. No user validation
// function is executed here; only the struct/tag parsing results and the
// declarations are used.
func (v *Validate) buildValidationPlan(typ reflect.Type, deps []FieldDependency) (*ValidationPlan, error) {
	sName := typ.Name()

	nodes := make(map[string]*depNode, len(deps))

	getNode := func(path string, order int) *depNode {
		n, ok := nodes[path]
		if !ok {
			n = &depNode{path: path, order: order}
			nodes[path] = n
		}
		return n
	}

	// Phase 1: resolve every declared path against the parsed struct fields.
	// Any unknown field is reported before the graph is even built, so no
	// cycle error can mask a bad declaration.
	type resolvedDep struct {
		field *depNode
		reads []*depNode
	}

	resolved := make([]resolvedDep, 0, len(deps))

	for _, d := range deps {
		fieldPath, fieldOrder, err := v.resolveFieldPath(typ, d.Field)
		if err != nil {
			return nil, &UnknownDependencyFieldError{StructName: sName, Path: d.Field}
		}

		rd := resolvedDep{field: getNode(fieldPath, fieldOrder)}

		for _, r := range d.Reads {
			readPath, readOrder, err := v.resolveFieldPath(typ, r)
			if err != nil {
				return nil, &UnknownDependencyFieldError{StructName: sName, Path: r}
			}

			rd.reads = append(rd.reads, getNode(readPath, readOrder))
		}

		resolved = append(resolved, rd)
	}

	// Phase 2: build the graph edges (deduplicated, deterministic order).
	for _, rd := range resolved {
		seen := make(map[string]struct{}, len(rd.reads))
		for _, read := range rd.reads {
			if _, ok := seen[read.path]; ok {
				continue
			}
			seen[read.path] = struct{}{}
			rd.field.reads = append(rd.field.reads, read.path)
		}
	}

	ordered := make([]*depNode, 0, len(nodes))
	for _, n := range nodes {
		ordered = append(ordered, n)
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].order != ordered[j].order {
			return ordered[i].order < ordered[j].order
		}
		return ordered[i].path < ordered[j].path
	})

	// Phase 3: cycle detection, reporting the concrete field chain.
	const (
		unvisited = iota
		inStack
		done
	)

	state := make(map[string]int, len(ordered))
	var stack []string

	var visit func(n *depNode) *DependencyCycleError
	visit = func(n *depNode) *DependencyCycleError {
		state[n.path] = inStack
		stack = append(stack, n.path)

		for _, r := range n.reads {
			switch state[r] {
			case inStack:
				// find the start of the cycle within the current stack
				start := 0
				for i, s := range stack {
					if s == r {
						start = i
						break
					}
				}
				chain := make([]string, 0, len(stack)-start+1)
				chain = append(chain, stack[start:]...)
				chain = append(chain, r)
				return &DependencyCycleError{StructName: sName, Chain: chain}
			case unvisited:
				if cerr := visit(nodes[r]); cerr != nil {
					return cerr
				}
			}
		}

		stack = stack[:len(stack)-1]
		state[n.path] = done
		return nil
	}

	for _, n := range ordered {
		if state[n.path] == unvisited {
			if cerr := visit(n); cerr != nil {
				return nil, cerr
			}
		}
	}

	// Phase 4: stable topological grouping (Kahn's algorithm by levels).
	inDegree := make(map[string]int, len(ordered))
	dependents := make(map[string][]string, len(ordered))
	for _, n := range ordered {
		inDegree[n.path] += len(n.reads)
		for _, r := range n.reads {
			dependents[r] = append(dependents[r], n.path)
		}
	}

	remaining := make(map[string]*depNode, len(ordered))
	for _, n := range ordered {
		remaining[n.path] = n
	}

	plan := &ValidationPlan{}

	for len(remaining) > 0 {
		var group []string
		for _, n := range ordered {
			if _, ok := remaining[n.path]; ok && inDegree[n.path] == 0 {
				group = append(group, n.path)
			}
		}

		if len(group) == 0 {
			// unreachable: cycle detection above already guarantees a DAG
			return nil, &DependencyCycleError{StructName: sName}
		}

		for _, path := range group {
			delete(remaining, path)
			for _, dep := range dependents[path] {
				inDegree[dep]--
			}
		}

		plan.Groups = append(plan.Groups, group)
	}

	return plan, nil
}

// resolveFieldPath resolves a declared dependency path against the parsed
// fields of typ using the validator's own naming rules: each segment matches
// either the Go field name or the alternate name produced by a registered
// TagNameFunc, and nested struct fields are traversed segment by segment.
// It returns the canonical path (built from the alternate names the validator
// reports) and the index of the top-level struct field the path starts at.
func (v *Validate) resolveFieldPath(typ reflect.Type, path string) (canonical string, topIdx int, err error) {
	segments := strings.Split(path, ".")

	canonicalSegments := make([]string, 0, len(segments))
	current := typ
	topIdx = -1

	for i, segment := range segments {
		cs := v.extractStructCache(reflect.New(current).Elem(), current.Name())

		var matched *cField
		for _, f := range cs.fields {
			if f.name == segment || f.altName == segment {
				matched = f
				break
			}
		}

		if matched == nil {
			return "", -1, fmt.Errorf("unknown field path segment '%s'", segment)
		}

		if i == 0 {
			topIdx = matched.idx
		}

		canonicalSegments = append(canonicalSegments, matched.altName)

		if i == len(segments)-1 {
			break
		}

		// descend into the nested struct field
		ft := current.Field(matched.idx).Type
		for ft.Kind() == reflect.Ptr {
			ft = ft.Elem()
		}

		_, hasCustomFunc := v.customFuncs[ft]
		if ft.Kind() != reflect.Struct || ft.ConvertibleTo(timeType) || hasCustomFunc {
			return "", -1, fmt.Errorf("field path segment '%s' is not a nested struct", segment)
		}

		current = ft
	}

	return strings.Join(canonicalSegments, "."), topIdx, nil
}
