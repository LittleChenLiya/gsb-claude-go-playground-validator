package validator

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
)

// StructFieldDeps declares the cross-field dependencies of a struct-level
// validation rule.
//
// Field is the name (or dotted path) of the field the rule validates — the
// field whose value is derived from the fields listed in Reads. Reads lists
// the fields (or dotted paths) the rule reads.
//
// Field paths follow the validator's naming rules: nested struct fields are
// joined with '.', and each segment is matched against the struct field's Go
// name or the alias produced by a registered TagNameFunc. For example, given:
//
//	type Order struct {
//	    Subtotal float64 `json:"subtotal"`
//	}
//
// both "Subtotal" and "subtotal" resolve to the same field.
type StructFieldDeps struct {
	Field string
	Reads []string
}

// StructValidationPlan is the stable, dependency-ordered grouping of the
// fields declared through RegisterStructValidationWithDeps.
//
// Groups are ordered by dependency: every field in group i only depends on
// fields in groups < i, so validating groups in order guarantees that a
// field's dependencies are validated first. The order of fields within a
// group is stable (sorted by canonical path).
type StructValidationPlan struct {
	groups [][]string
}

// Groups returns the field groups in dependency order.
func (p *StructValidationPlan) Groups() [][]string {
	if p == nil {
		return nil
	}
	return p.groups
}

// ErrStructFieldNotFound is returned when a field referenced by a
// StructFieldDeps declaration does not exist in the target struct.
type ErrStructFieldNotFound struct {
	Type  reflect.Type
	Field string
}

func (e *ErrStructFieldNotFound) Error() string {
	return fmt.Sprintf("validator: struct %q has no field %q", e.Type.String(), e.Field)
}

// ErrStructDepsCycle is returned when the dependency graph built from
// StructFieldDeps declarations contains a cycle.
type ErrStructDepsCycle struct {
	Type  reflect.Type
	Chain []string
}

func (e *ErrStructDepsCycle) Error() string {
	return fmt.Sprintf("validator: struct %q has a dependency cycle: %s", e.Type.String(), strings.Join(e.Chain, " -> "))
}

// structCacheForType returns the parsed struct cache for the given type,
// creating it if necessary. It reuses the same field-resolution results as
// validation without executing any validation functions.
func (v *Validate) structCacheForType(typ reflect.Type) *cStruct {
	current := reflect.New(typ).Elem()
	return v.extractStructCache(current, typ.Name())
}

// resolveFieldPath resolves a dotted field path to a canonical Go field path
// using the struct's parsed fields. Each segment is matched against the Go
// field name or the alias produced by a registered TagNameFunc. It returns the
// canonical path (Go names joined by '.') and whether the full path resolved.
func (v *Validate) resolveFieldPath(typ reflect.Type, path string) (string, bool) {
	if path == "" {
		return "", false
	}

	segments := strings.Split(path, namespaceSeparator)
	canonical := make([]string, 0, len(segments))

	currentType := typ
	for _, seg := range segments {
		cs := v.structCacheForType(currentType)

		var found *cField
		for _, f := range cs.fields {
			if f.name == seg || f.altName == seg {
				found = f
				break
			}
		}
		if found == nil {
			return "", false
		}

		canonical = append(canonical, found.name)

		fld, ok := currentType.FieldByName(found.name)
		if !ok {
			return "", false
		}

		currentType = fld.Type
		for currentType.Kind() == reflect.Ptr {
			currentType = currentType.Elem()
		}
	}

	return strings.Join(canonical, namespaceSeparator), true
}

// resolveDeps resolves and validates a single deps declaration against the
// struct type, returning the canonical graph edges. It does not execute fn.
// Unknown fields are reported before any cycle check.
func (v *Validate) resolveDeps(typ reflect.Type, deps StructFieldDeps) (map[string][]string, error) {
	fieldPath, ok := v.resolveFieldPath(typ, deps.Field)
	if !ok {
		return nil, &ErrStructFieldNotFound{Type: typ, Field: deps.Field}
	}

	edges := map[string][]string{fieldPath: nil}

	for _, read := range deps.Reads {
		readPath, ok := v.resolveFieldPath(typ, read)
		if !ok {
			return nil, &ErrStructFieldNotFound{Type: typ, Field: read}
		}
		edges[fieldPath] = append(edges[fieldPath], readPath)
		if _, exists := edges[readPath]; !exists {
			edges[readPath] = nil
		}
	}

	return edges, nil
}

// addEdges merges deps into graph without creating duplicate edges. The node
// is always created even when it has no deps, so that root nodes appear in the
// graph.
func addEdges(graph map[string][]string, node string, deps []string) {
	if _, ok := graph[node]; !ok {
		graph[node] = nil
	}
	existing := make(map[string]struct{}, len(graph[node]))
	for _, d := range graph[node] {
		existing[d] = struct{}{}
	}
	for _, d := range deps {
		if _, ok := existing[d]; !ok {
			graph[node] = append(graph[node], d)
			existing[d] = struct{}{}
		}
	}
}

// detectCycle returns the concrete field chain of a cycle in the dependency
// graph, or nil if the graph is acyclic. The chain starts and ends on the
// same field, e.g. ["items", "total", "subtotal", "items"].
func detectCycle(graph map[string][]string) []string {
	const (
		white = 0
		gray  = 1
		black = 2
	)

	color := make(map[string]int)
	var stack []string

	var dfs func(node string) []string
	dfs = func(node string) []string {
		color[node] = gray
		stack = append(stack, node)

		for _, dep := range graph[node] {
			if color[dep] == gray {
				i := 0
				for stack[i] != dep {
					i++
				}
				chain := append([]string{}, stack[i:]...)
				chain = append(chain, dep)
				return chain
			}
			if color[dep] == white {
				if chain := dfs(dep); chain != nil {
					return chain
				}
			}
		}

		color[node] = black
		stack = stack[:len(stack)-1]
		return nil
	}

	for node := range graph {
		if color[node] == white {
			if chain := dfs(node); chain != nil {
				return chain
			}
		}
	}
	return nil
}

// topoGroups returns the stable, dependency-ordered groups of fields. Every
// field in group i only depends on fields in groups < i. Returns nil if the
// graph contains a cycle.
func topoGroups(graph map[string][]string) [][]string {
	outDegree := make(map[string]int, len(graph))
	dependents := make(map[string][]string, len(graph))

	for node, deps := range graph {
		outDegree[node] = len(deps)
		for _, dep := range deps {
			dependents[dep] = append(dependents[dep], node)
		}
	}

	var groups [][]string
	remaining := len(graph)

	for remaining > 0 {
		var group []string
		for node, deg := range outDegree {
			if deg == 0 {
				group = append(group, node)
			}
		}
		if len(group) == 0 {
			return nil // cycle
		}

		sort.Strings(group)
		groups = append(groups, group)

		for _, node := range group {
			for _, dep := range dependents[node] {
				outDegree[dep]--
			}
			outDegree[node] = -1
			remaining--
		}
	}

	return groups
}

// buildStructValidationPlan builds the plan from the accumulated canonical
// graph, checking for cycles.
func buildStructValidationPlan(typ reflect.Type, graph map[string][]string) (*StructValidationPlan, error) {
	if chain := detectCycle(graph); chain != nil {
		return nil, &ErrStructDepsCycle{Type: typ, Chain: chain}
	}
	return &StructValidationPlan{groups: topoGroups(graph)}, nil
}
