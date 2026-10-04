package validator

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	. "github.com/go-playground/assert/v2"
)

type depTestItem struct {
	Price float64 `json:"price"`
}

type depTestOrder struct {
	Items    []depTestItem `json:"items"`
	Subtotal float64       `json:"subtotal"`
	Total    float64       `json:"total"`
}

func newDepTestValidate() *Validate {
	v := New()
	v.RegisterTagNameFunc(func(fld reflect.StructField) string {
		name := strings.SplitN(fld.Tag.Get("json"), ",", 2)[0]
		if name == "-" {
			return ""
		}
		return name
	})
	return v
}

func TestStructValidationDepsPlan(t *testing.T) {
	v := newDepTestValidate()

	ruleCalled := false
	fn := func(sl StructLevel) {
		ruleCalled = true
	}

	deps := []FieldDependency{
		{Field: "subtotal", Reads: []string{"items"}},
		{Field: "total", Reads: []string{"subtotal"}},
	}

	err := v.RegisterStructValidationWithDeps(fn, deps, depTestOrder{})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	// registration-time graph building must not execute the user rule
	Equal(t, ruleCalled, false)

	plan, ok := v.StructValidationPlan(depTestOrder{})
	Equal(t, ok, true)
	Equal(t, ruleCalled, false)

	expected := [][]string{{"items"}, {"subtotal"}, {"total"}}
	Equal(t, len(plan.Groups), len(expected))
	for i := range expected {
		Equal(t, plan.Groups[i], expected[i])
	}

	// pointer types resolve to the same plan
	planPtr, ok := v.StructValidationPlan(&depTestOrder{})
	Equal(t, ok, true)
	Equal(t, planPtr, plan)

	// the registered rule still runs during actual validation
	err = v.Struct(depTestOrder{})
	Equal(t, err, nil)
	Equal(t, ruleCalled, true)
}

func TestStructValidationDepsCycle(t *testing.T) {
	v := newDepTestValidate()

	fn := func(sl StructLevel) {
		t.Fatal("user rule must not be executed during registration")
	}

	deps := []FieldDependency{
		{Field: "subtotal", Reads: []string{"items"}},
		{Field: "total", Reads: []string{"subtotal"}},
		{Field: "items", Reads: []string{"total"}},
	}

	err := v.RegisterStructValidationWithDeps(fn, deps, depTestOrder{})
	if err == nil {
		t.Fatal("expected a cycle error, got nil")
	}

	var cycleErr *DependencyCycleError
	if !errors.As(err, &cycleErr) {
		t.Fatalf("expected *DependencyCycleError, got %T: %v", err, err)
	}

	// the chain must contain exactly the three nodes of the cycle,
	// with the first node repeated at the end
	Equal(t, len(cycleErr.Chain), 4)
	Equal(t, cycleErr.Chain[0], cycleErr.Chain[3])

	seen := map[string]bool{}
	for _, node := range cycleErr.Chain[:3] {
		seen[node] = true
	}
	Equal(t, len(seen), 3)
	Equal(t, seen["items"], true)
	Equal(t, seen["subtotal"], true)
	Equal(t, seen["total"], true)

	// nothing must have been registered on failure
	_, ok := v.StructValidationPlan(depTestOrder{})
	Equal(t, ok, false)
}

func TestStructValidationDepsUnknownFieldReportedFirst(t *testing.T) {
	v := newDepTestValidate()

	fn := func(sl StructLevel) {
		t.Fatal("user rule must not be executed during registration")
	}

	// contains both a cycle (items <-> total) and an unknown field;
	// the unknown field must be reported first
	deps := []FieldDependency{
		{Field: "total", Reads: []string{"items"}},
		{Field: "items", Reads: []string{"total"}},
		{Field: "subtotal", Reads: []string{"does_not_exist"}},
	}

	err := v.RegisterStructValidationWithDeps(fn, deps, depTestOrder{})
	if err == nil {
		t.Fatal("expected an unknown field error, got nil")
	}

	var unknownErr *UnknownDependencyFieldError
	if !errors.As(err, &unknownErr) {
		t.Fatalf("expected *UnknownDependencyFieldError, got %T: %v", err, err)
	}

	Equal(t, unknownErr.Path, "does_not_exist")
	Equal(t, unknownErr.StructName, "depTestOrder")

	// unknown nested path segment must also be reported
	err = v.RegisterStructValidationWithDeps(fn, []FieldDependency{
		{Field: "total", Reads: []string{"items.missing"}},
	}, depTestOrder{})

	var nestedUnknown *UnknownDependencyFieldError
	if !errors.As(err, &nestedUnknown) {
		t.Fatalf("expected *UnknownDependencyFieldError, got %T: %v", err, err)
	}
	Equal(t, nestedUnknown.Path, "items.missing")
}

type depTestAddress struct {
	Street string `json:"street"`
}

type depTestShipping struct {
	Address depTestAddress `json:"address"`
}

type depTestInvoice struct {
	Shipping depTestShipping `json:"shipping"`
	Discount float64         `json:"discount"`
	Total    float64         `json:"total"`
}

func TestStructValidationDepsNestedAliasPaths(t *testing.T) {
	v := newDepTestValidate()

	ruleCalled := false
	fn := func(ctx context.Context, sl StructLevel) {
		ruleCalled = true
	}

	// declare dependencies using a mix of Go field names and json alias
	// names, including a dotted nested struct path
	deps := []FieldDependency{
		{Field: "total", Reads: []string{"shipping.address.street", "discount"}},
		{Field: "discount", Reads: []string{"Shipping.Address"}},
	}

	err := v.RegisterStructValidationWithDepsCtx(fn, deps, &depTestInvoice{})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	Equal(t, ruleCalled, false)

	plan, ok := v.StructValidationPlan(depTestInvoice{})
	Equal(t, ok, true)

	// canonical paths use the validator's alternate (json) names
	expected := [][]string{
		{"shipping.address", "shipping.address.street"},
		{"discount"},
		{"total"},
	}
	Equal(t, len(plan.Groups), len(expected))
	for i := range expected {
		Equal(t, plan.Groups[i], expected[i])
	}

	Equal(t, plan.Contains("shipping.address.street"), true)
	Equal(t, plan.Contains("Shipping.Address.Street"), false)

	// the registered ctx rule still runs during actual validation
	err = v.Struct(depTestInvoice{})
	Equal(t, err, nil)
	Equal(t, ruleCalled, true)
}

func TestStructValidationDepsNestedCycle(t *testing.T) {
	v := newDepTestValidate()

	fn := func(sl StructLevel) {
		t.Fatal("user rule must not be executed during registration")
	}

	deps := []FieldDependency{
		{Field: "shipping.address", Reads: []string{"total"}},
		{Field: "total", Reads: []string{"shipping.address"}},
	}

	err := v.RegisterStructValidationWithDeps(fn, deps, depTestInvoice{})
	if err == nil {
		t.Fatal("expected a cycle error, got nil")
	}

	var cycleErr *DependencyCycleError
	if !errors.As(err, &cycleErr) {
		t.Fatalf("expected *DependencyCycleError, got %T: %v", err, err)
	}

	Equal(t, len(cycleErr.Chain), 3)
	Equal(t, cycleErr.Chain[0], cycleErr.Chain[2])
	Equal(t, cycleErr.Chain[1] == "total" || cycleErr.Chain[1] == "shipping.address", true)
}

func TestStructValidationDepsSelfCycle(t *testing.T) {
	v := newDepTestValidate()

	fn := func(sl StructLevel) {}

	err := v.RegisterStructValidationWithDeps(fn, []FieldDependency{
		{Field: "total", Reads: []string{"total"}},
	}, depTestOrder{})
	if err == nil {
		t.Fatal("expected a cycle error, got nil")
	}

	var cycleErr *DependencyCycleError
	if !errors.As(err, &cycleErr) {
		t.Fatalf("expected *DependencyCycleError, got %T: %v", err, err)
	}
	Equal(t, cycleErr.Chain, []string{"total", "total"})
}

func TestStructValidationDepsChecksDoNotExecuteUserRules(t *testing.T) {
	v := newDepTestValidate()

	calls := 0
	fn := func(sl StructLevel) {
		calls++
	}

	deps := []FieldDependency{
		{Field: "subtotal", Reads: []string{"items"}},
		{Field: "total", Reads: []string{"subtotal"}},
	}

	// valid registration
	err := v.RegisterStructValidationWithDeps(fn, deps, depTestOrder{})
	Equal(t, err, nil)
	Equal(t, calls, 0)

	// plan retrieval
	_, ok := v.StructValidationPlan(depTestOrder{})
	Equal(t, ok, true)
	Equal(t, calls, 0)

	// failing registrations (unknown field + cycle) on a fresh validator
	v2 := newDepTestValidate()
	err = v2.RegisterStructValidationWithDeps(fn, []FieldDependency{
		{Field: "total", Reads: []string{"nope"}},
	}, depTestOrder{})
	if err == nil {
		t.Fatal("expected unknown field error")
	}
	Equal(t, calls, 0)

	err = v2.RegisterStructValidationWithDeps(fn, []FieldDependency{
		{Field: "items", Reads: []string{"total"}},
		{Field: "total", Reads: []string{"items"}},
	}, depTestOrder{})
	if err == nil {
		t.Fatal("expected cycle error")
	}
	Equal(t, calls, 0)
}

func TestRegisterStructValidationWithoutDepsUnchanged(t *testing.T) {
	v := newDepTestValidate()

	ruleCalled := false
	v.RegisterStructValidation(func(sl StructLevel) {
		ruleCalled = true
	}, depTestOrder{})

	// old-style registration provides no plan
	_, ok := v.StructValidationPlan(depTestOrder{})
	Equal(t, ok, false)

	// default validation behaviour is unchanged
	err := v.Struct(depTestOrder{})
	Equal(t, err, nil)
	Equal(t, ruleCalled, true)
}

func TestStructValidationDepsErrorStrings(t *testing.T) {
	unknown := &UnknownDependencyFieldError{StructName: "Order", Path: "nope"}
	Equal(t, strings.Contains(unknown.Error(), "Order"), true)
	Equal(t, strings.Contains(unknown.Error(), "nope"), true)

	cycle := &DependencyCycleError{StructName: "Order", Chain: []string{"a", "b", "a"}}
	Equal(t, cycle.Error(), "validator: field dependency cycle detected in struct 'Order': a -> b -> a")
}
