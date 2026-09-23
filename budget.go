package validator

import (
	"context"
	"errors"
	"reflect"
)

// BudgetStopReason identifies why a StructWithBudget call stopped traversing.
type BudgetStopReason uint8

const (
	// BudgetStopNone means traversal ran to completion; when ValidationErrors
	// are present they represent the complete set of validation failures.
	BudgetStopNone BudgetStopReason = iota
	// BudgetStopMaxErrors means the error budget was reached and traversal
	// stopped immediately; collected errors are a truncated prefix, not the
	// total number of failures.
	BudgetStopMaxErrors
	// BudgetStopMaxNodes means the node visit budget was exhausted; neither
	// the next custom validator nor any struct-level callback was invoked
	// past the boundary.
	BudgetStopMaxNodes
	// BudgetStopContextCanceled means the context was canceled (or its
	// deadline exceeded) during validation; the callback that canceled it was
	// allowed to finish, but no further node was entered.
	BudgetStopContextCanceled
	// BudgetStopCycle means the same pointer was encountered again on the
	// active traversal path. Shared sub-objects reached through different
	// branches are not cycles and are charged as separate node visits.
	BudgetStopCycle
	// BudgetStopInvalidInput means the budget or the input value was rejected
	// before (or while) traversing; inspect the returned error for details.
	BudgetStopInvalidInput
)

// String returns a human readable representation of the stop reason.
func (r BudgetStopReason) String() string {
	switch r {
	case BudgetStopNone:
		return "none"
	case BudgetStopMaxErrors:
		return "max-errors"
	case BudgetStopMaxNodes:
		return "max-nodes"
	case BudgetStopContextCanceled:
		return "context-canceled"
	case BudgetStopCycle:
		return "cycle"
	case BudgetStopInvalidInput:
		return "invalid-input"
	default:
		return "unknown"
	}
}

var (
	// ErrInvalidBudget is returned by StructWithBudget when MaxNodes or
	// MaxErrors is negative. Zero is valid and means that category of work
	// is not allowed at all; nil means unlimited.
	ErrInvalidBudget = errors.New("validator: StructWithBudget budgets must be nil (unlimited) or >= 0")

	// ErrNonStringMapKey is returned by StructWithBudget when a dive traverses
	// a map whose key kind is not string. Only string-keyed maps are supported
	// so that iteration order, and therefore budget consumption, is
	// deterministic (keys are visited in sorted order).
	ErrNonStringMapKey = errors.New("validator: StructWithBudget only supports diving into maps with string keys")
)

// StructBudget limits the amount of work StructWithBudget performs.
//
// MaxNodes caps the number of node visits: every entry into a struct field
// or a dive container element counts as one visit. The root struct itself is
// not counted, and the same object reached through different paths is charged
// once per path. A nil budget means unlimited; zero means no node visits are
// allowed at all.
//
// MaxErrors caps the number of collected validation errors. Once the cap is
// reached traversal stops immediately and the result is marked truncated, so
// the returned errors must not be treated as the total set of failures. A nil
// budget means unlimited; zero means no errors may be collected.
type StructBudget struct {
	MaxNodes  *int
	MaxErrors *int
}

// StructBudgetResult is the outcome of a StructWithBudget call.
type StructBudgetResult struct {
	// Errors holds the validation errors collected before traversal stopped.
	// Unless StopReason is BudgetStopNone this is a partial prefix and must
	// not be reported as the complete validation outcome.
	Errors ValidationErrors
	// StopReason reports why traversal stopped, independent of whether any
	// validation failed.
	StopReason BudgetStopReason
	// NodesVisited is the number of struct field / dive element visits that
	// were charged against the node budget.
	NodesVisited int
	// Truncated is true when collection stopped because the error budget was
	// reached; Errors is then a prefix and the total error count is unknown.
	Truncated bool
}

// budgetState is the per-validation mutable budget bookkeeping. It is only
// ever attached to a pooled validate for the duration of a StructWithBudget
// call and is always detached before the validate returns to the pool.
type budgetState struct {
	maxNodes   int // -1 when unlimited
	maxErrors  int // -1 when unlimited
	nodes      int
	stopReason BudgetStopReason
	truncated  bool
	fatalErr   error
	active     map[uintptr]int // pointers on the active traversal path
}

// budgetHalt reports whether traversal must stop, recording the reason on
// first stop. The currently running callback is never interrupted; the halt
// takes effect before the next node, validator or struct-level callback.
func (v *validate) budgetHalt(ctx context.Context) bool {
	b := v.budget
	if b.stopReason != BudgetStopNone {
		return true
	}
	if b.maxErrors >= 0 && len(v.errs) >= b.maxErrors {
		b.stopReason = BudgetStopMaxErrors
		b.truncated = true
		return true
	}
	if ctx.Err() != nil {
		b.stopReason = BudgetStopContextCanceled
		return true
	}
	return false
}

// enterNode charges one node visit (struct field or dive container element).
// It returns false when traversal must stop instead of entering the node.
func (v *validate) enterNode(ctx context.Context) bool {
	if v.budgetHalt(ctx) {
		return false
	}
	b := v.budget
	if b.maxNodes >= 0 && b.nodes >= b.maxNodes {
		b.stopReason = BudgetStopMaxNodes
		return false
	}
	b.nodes++
	return true
}

// appendError records a validation error honoring the error budget. When the
// budget is exhausted the error is dropped, the result is marked truncated
// and traversal unwinds. With no budget attached it behaves exactly like
// appending to v.errs, preserving legacy Struct/StructCtx behavior.
func (v *validate) appendError(fe *fieldError) {
	if b := v.budget; b != nil {
		if b.stopReason != BudgetStopNone {
			return
		}
		if b.maxErrors >= 0 && len(v.errs) >= b.maxErrors {
			b.stopReason = BudgetStopMaxErrors
			b.truncated = true
			return
		}
	}
	v.errs = append(v.errs, fe)
}

// budgetTrackPtrs registers every non-nil pointer in the dereference chain
// of current on the active path and reports a cycle when one is already active.
// The returned function unregisters the pointers and must be called when the
// node is done; it is nil when nothing was tracked.
func (v *validate) budgetTrackPtrs(current reflect.Value) (func(), bool) {
	b := v.budget
	var tracked []uintptr
	for {
		switch current.Kind() {
		case reflect.Interface:
			if current.IsNil() {
				return untrackPtrs(b, tracked), true
			}
			current = current.Elem()
		case reflect.Ptr:
			if current.IsNil() {
				return untrackPtrs(b, tracked), true
			}
			ptr := current.Pointer()
			if b.active[ptr] > 0 {
				b.stopReason = BudgetStopCycle
				if untrack := untrackPtrs(b, tracked); untrack != nil {
					untrack()
				}
				return nil, false
			}
			b.active[ptr] = 1
			tracked = append(tracked, ptr)
			current = current.Elem()
		default:
			return untrackPtrs(b, tracked), true
		}
	}
}

func untrackPtrs(b *budgetState, tracked []uintptr) func() {
	if len(tracked) == 0 {
		return nil
	}
	return func() {
		for _, ptr := range tracked {
			delete(b.active, ptr)
		}
	}
}

// StructWithBudget validates a struct's exposed fields like StructCtx, but
// stops early once the provided budget is exhausted and reports why.
//
// Semantics that differ from StructCtx:
//
//   - Every entry into a struct field or dive container element is charged
//     against MaxNodes; the root struct itself is free and the same object
//     reached via different paths is charged once per path. Once the boundary
//     is reached neither the next custom validator nor any struct-level
//     callback is invoked.
//   - Once MaxErrors have been collected traversal stops immediately, the
//     result is marked Truncated and Errors is only a prefix of the failures;
//     never report it as the total error count.
//   - A canceled context lets the currently running callback finish but no
//     further node is entered; StopReason distinguishes this from ordinary
//     validation failures and budget exhaustion.
//   - map[string] dive elements are visited in sorted key order so the same
//     budget always lands on the same fields; maps with non-string keys are
//     rejected with ErrNonStringMapKey. Slice and struct order is unchanged.
//   - Encountering the same pointer twice on the active traversal path stops
//     with BudgetStopCycle; shared sub-objects on different branches are not
//     cycles.
//
// The caller's value and the Validate instance (its registrations and caches)
// are never modified, and the pooled validation state is left clean for the
// next call. Struct and StructCtx are unaffected and behave as before.
func (v *Validate) StructWithBudget(ctx context.Context, s interface{}, budget StructBudget) (StructBudgetResult, error) {
	var res StructBudgetResult

	if ctx == nil {
		ctx = context.Background()
	}

	bs := &budgetState{
		maxNodes:   -1,
		maxErrors:  -1,
		active:     make(map[uintptr]int),
		stopReason: BudgetStopNone,
	}
	if budget.MaxNodes != nil {
		if *budget.MaxNodes < 0 {
			res.StopReason = BudgetStopInvalidInput
			return res, ErrInvalidBudget
		}
		bs.maxNodes = *budget.MaxNodes
	}
	if budget.MaxErrors != nil {
		if *budget.MaxErrors < 0 {
			res.StopReason = BudgetStopInvalidInput
			return res, ErrInvalidBudget
		}
		bs.maxErrors = *budget.MaxErrors
	}

	val := reflect.ValueOf(s)
	top := val

	if val.Kind() == reflect.Ptr && !val.IsNil() {
		val = val.Elem()
	}

	if val.Kind() != reflect.Struct || val.Type().ConvertibleTo(timeType) {
		res.StopReason = BudgetStopInvalidInput
		return res, &InvalidValidationError{Type: reflect.TypeOf(s)}
	}

	vd := v.pool.Get().(*validate)
	vd.top = top
	vd.isPartial = false
	vd.budget = bs

	if top.Kind() == reflect.Ptr && !top.IsNil() {
		bs.active[top.Pointer()] = 1
	}

	vd.validateStruct(ctx, top, val, val.Type(), vd.ns[0:0], vd.actualNs[0:0], nil)

	// detach all per-call state so the pooled validate is clean for reuse
	vd.budget = nil
	if len(vd.errs) > 0 {
		res.Errors = vd.errs
		vd.errs = nil
	}
	v.pool.Put(vd)

	res.StopReason = bs.stopReason
	res.NodesVisited = bs.nodes
	res.Truncated = bs.truncated

	if bs.fatalErr != nil {
		return res, bs.fatalErr
	}
	return res, nil
}
