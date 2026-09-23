package validator

import (
	"context"
	"fmt"
	"reflect"
)

// StopReason identifies why a StructWithBudget call stopped traversing the
// object graph. It is independent from the collected ValidationErrors so
// that ordinary validation failures, budget exhaustion and context
// cancellation can be told apart.
type StopReason int

const (
	// StopNone indicates traversal ran to completion. ValidationErrors may
	// still be non-empty: ordinary validation failures are reported through
	// the returned errors, not through the stop reason.
	StopNone StopReason = iota
	// StopMaxNodes indicates the node budget was exhausted before the whole
	// object graph could be visited.
	StopMaxNodes
	// StopMaxErrors indicates the error budget was reached; the returned
	// errors are truncated and do not represent the total number of failures.
	StopMaxErrors
	// StopContextCanceled indicates the provided context was canceled; the
	// currently running callback was allowed to finish, but no further node
	// was entered.
	StopContextCanceled
	// StopCycle indicates the same pointer was encountered twice on the
	// active traversal path. Shared sub-objects reached through different
	// branches are not cycles.
	StopCycle
	// StopError indicates traversal was aborted because of an error, such as
	// an unsupported map key type; see the returned error for details.
	StopError
)

// String returns a human readable representation of the StopReason.
func (r StopReason) String() string {
	switch r {
	case StopNone:
		return "None"
	case StopMaxNodes:
		return "MaxNodes"
	case StopMaxErrors:
		return "MaxErrors"
	case StopContextCanceled:
		return "ContextCanceled"
	case StopCycle:
		return "Cycle"
	case StopError:
		return "Error"
	default:
		return "Unknown"
	}
}

// Budget limits how much work a single StructWithBudget call performs.
//
// A zero value for a field means that kind of work is not allowed at all;
// negative values are rejected with an error.
type Budget struct {
	// MaxNodes caps how many nodes may be visited. Entering a struct field
	// or a dive container element counts as one visit; the root struct
	// itself is not counted. The same object reached through different
	// paths is counted once per path.
	MaxNodes int64
	// MaxErrors caps how many validation errors may be collected before
	// traversal stops and the result is marked truncated.
	MaxErrors int64
}

// StopResult describes how a StructWithBudget call ended.
type StopResult struct {
	// Reason is the independent stop reason; StopNone means traversal
	// completed (ordinary validation failures are still possible and are
	// reported via the returned ValidationErrors).
	Reason StopReason
	// Truncated is true when traversal stopped early and therefore the
	// returned errors do not represent the total number of failures.
	Truncated bool
	// Nodes is the number of field/element visits performed.
	Nodes int64
	// Path is the namespace at which a cycle was detected, when
	// Reason == StopCycle.
	Path string
}

// visit identifies a pointer on the active traversal path for cycle detection.
type visit struct {
	typ reflect.Type
	ptr uintptr
}

// budgetState holds the per-call budget traversal state. It is stored on the
// pooled validate only for the duration of a StructWithBudget call and is
// always cleared before the validate is returned to the pool.
type budgetState struct {
	ctx       context.Context
	maxNodes  int64
	maxErrors int64
	nodes     int64
	stop      StopReason
	truncated bool
	cyclePath string
	err       error
	active    map[visit]struct{}
}

// budgetEnter is called before entering a struct field or dive container
// element. It reports whether traversal may proceed into the node.
func (v *validate) budgetEnter() bool {
	b := v.budget
	if b.stop != StopNone {
		return false
	}
	if b.ctx != nil && b.ctx.Err() != nil {
		b.stop = StopContextCanceled
		return false
	}
	if int64(len(v.errs)) >= b.maxErrors {
		b.stop = StopMaxErrors
		b.truncated = true
		return false
	}
	if b.nodes >= b.maxNodes {
		b.stop = StopMaxNodes
		b.truncated = true
		return false
	}
	b.nodes++
	return true
}

// budgetStopped reports whether budget traversal has been stopped.
func (v *validate) budgetStopped() bool {
	return v.budget != nil && v.budget.stop != StopNone
}

// budgetTrackPointer tracks pointers to structs and containers on the active
// traversal path so that re-encountering the same pointer is reported as a
// cycle instead of recursing forever. Shared sub-objects reached through
// different branches are not cycles, because a pointer is only tracked while
// its branch is active. It returns a pop function removing the pointer from
// the active path, and whether a cycle was detected.
func (v *validate) budgetTrackPointer(current reflect.Value, ns []byte, cf *cField) (pop func(), cycle bool) {
	for current.Kind() == reflect.Interface && !current.IsNil() {
		current = current.Elem()
	}

	if current.Kind() != reflect.Ptr || current.IsNil() {
		return nil, false
	}

	et := current.Type().Elem()
	for et.Kind() == reflect.Ptr {
		et = et.Elem()
	}

	switch et.Kind() {
	case reflect.Struct, reflect.Map, reflect.Slice, reflect.Array:
	default:
		return nil, false
	}

	vis := visit{typ: current.Type(), ptr: current.Pointer()}
	if _, ok := v.budget.active[vis]; ok {
		v.budget.stop = StopCycle
		v.budget.truncated = true
		v.budget.cyclePath = appendAltName(ns, cf.altName)
		return nil, true
	}

	v.budget.active[vis] = struct{}{}
	return func() { delete(v.budget.active, vis) }, false
}

// StructWithBudget validates a structs exposed fields like StructCtx, but
// limits the amount of work performed according to the provided Budget.
//
// It returns the ValidationErrors collected before stopping, an independent
// StopResult describing why traversal stopped, and an error for invalid
// input (negative budgets, non-struct values or unsupported map key types).
//
// Unlike Struct/StructCtx, map[string] containers are traversed in sorted
// key order so that a given budget always lands on the same fields; slices
// and structs keep their natural order. Maps with non-string keys are
// explicitly rejected. Aliases, dive and custom tags behave as usual.
//
// When the result is truncated the returned errors are only a prefix of the
// failures and must not be treated as the total error count.
func (v *Validate) StructWithBudget(ctx context.Context, s interface{}, budget Budget) (ValidationErrors, StopResult, error) {
	if budget.MaxNodes < 0 || budget.MaxErrors < 0 {
		return nil, StopResult{}, fmt.Errorf("validator: budget values must be >= 0, got MaxNodes=%d MaxErrors=%d", budget.MaxNodes, budget.MaxErrors)
	}
	if ctx == nil {
		ctx = context.Background()
	}

	val := reflect.ValueOf(s)
	top := val

	if val.Kind() == reflect.Ptr && !val.IsNil() {
		val = val.Elem()
	}

	if val.Kind() != reflect.Struct || val.Type().ConvertibleTo(timeType) {
		return nil, StopResult{}, &InvalidValidationError{Type: reflect.TypeOf(s)}
	}

	vd := v.pool.Get().(*validate)
	vd.top = top
	vd.isPartial = false

	bs := &budgetState{
		ctx:       ctx,
		maxNodes:  budget.MaxNodes,
		maxErrors: budget.MaxErrors,
		active:    make(map[visit]struct{}),
	}
	vd.budget = bs

	vd.validateStruct(ctx, top, val, val.Type(), vd.ns[0:0], vd.actualNs[0:0], nil)

	// always leave the pooled validate in a clean state, even when traversal
	// stopped early, so the next call starts without leftover errors or state.
	errs := vd.errs
	vd.errs = nil
	vd.budget = nil
	v.pool.Put(vd)

	res := StopResult{
		Reason:    bs.stop,
		Truncated: bs.truncated,
		Nodes:     bs.nodes,
		Path:      bs.cyclePath,
	}
	if bs.err != nil {
		return errs, res, bs.err
	}
	return errs, res, nil
}
