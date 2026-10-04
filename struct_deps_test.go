package validator

import (
	"reflect"
	"strings"
	"testing"

	. "github.com/go-playground/assert/v2"
)

type orderItem struct {
	Price float64
	Qty   int
}

type order struct {
	Items    []orderItem
	Subtotal float64
	Total    float64
}

type orderAddress struct {
	Street string `json:"street"`
	City   string `json:"city"`
}

type orderWithAddress struct {
	Items    []orderItem   `json:"items"`
	Subtotal float64       `json:"subtotal"`
	Total    float64       `json:"total"`
	Address  *orderAddress `json:"address"`
}

func jsonTagNameFunc(fld reflect.StructField) string {
	name, _, _ := strings.Cut(fld.Tag.Get("json"), ",")
	if name == "-" {
		return ""
	}
	return name
}

// TestStructValidationDepsPlan verifies that legal dependencies produce a
// stable, dependency-ordered plan.
func TestStructValidationDepsPlan(t *testing.T) {
	v := New()

	// subtotal depends on items
	plan, err := v.RegisterStructValidationWithDeps(
		func(sl StructLevel) {},
		StructFieldDeps{Field: "Subtotal", Reads: []string{"Items"}},
		order{},
	)
	Equal(t, err, nil)
	NotEqual(t, plan, nil)
	Equal(t, plan.Groups(), [][]string{{"Items"}, {"Subtotal"}})

	// total depends on subtotal -> items, subtotal, total
	plan, err = v.RegisterStructValidationWithDeps(
		func(sl StructLevel) {},
		StructFieldDeps{Field: "Total", Reads: []string{"Subtotal"}},
		order{},
	)
	Equal(t, err, nil)
	Equal(t, plan.Groups(), [][]string{{"Items"}, {"Subtotal"}, {"Total"}})
}

// TestStructValidationDepsPlanStable verifies the plan is deterministic.
func TestStructValidationDepsPlanStable(t *testing.T) {
	v := New()

	_, err := v.RegisterStructValidationWithDeps(
		func(sl StructLevel) {},
		StructFieldDeps{Field: "Subtotal", Reads: []string{"Items"}},
		order{},
	)
	Equal(t, err, nil)
	_, err = v.RegisterStructValidationWithDeps(
		func(sl StructLevel) {},
		StructFieldDeps{Field: "Total", Reads: []string{"Subtotal"}},
		order{},
	)
	Equal(t, err, nil)

	p1, err := v.StructValidationPlan(order{})
	Equal(t, err, nil)
	p2, err := v.StructValidationPlan(order{})
	Equal(t, err, nil)
	Equal(t, p1.Groups(), p2.Groups())
}

// TestStructValidationDepsCycle verifies that a dependency cycle is rejected
// with the concrete field chain.
func TestStructValidationDepsCycle(t *testing.T) {
	v := New()

	_, err := v.RegisterStructValidationWithDeps(
		func(sl StructLevel) {},
		StructFieldDeps{Field: "Subtotal", Reads: []string{"Items"}},
		order{},
	)
	Equal(t, err, nil)
	_, err = v.RegisterStructValidationWithDeps(
		func(sl StructLevel) {},
		StructFieldDeps{Field: "Total", Reads: []string{"Subtotal"}},
		order{},
	)
	Equal(t, err, nil)

	// items depends on total -> three-node cycle
	_, err = v.RegisterStructValidationWithDeps(
		func(sl StructLevel) {},
		StructFieldDeps{Field: "Items", Reads: []string{"Total"}},
		order{},
	)
	NotEqual(t, err, nil)

	cycle, ok := err.(*ErrStructDepsCycle)
	Equal(t, ok, true)
	NotEqual(t, cycle, nil)

	// three unique nodes, chain closes on itself
	Equal(t, len(cycle.Chain), 4)
	Equal(t, cycle.Chain[0], cycle.Chain[3])

	expected := map[string]bool{"Items": true, "Subtotal": true, "Total": true}
	for _, n := range cycle.Chain[:3] {
		_, ok := expected[n]
		Equal(t, ok, true)
	}
}

// TestStructValidationDepsSelfCycle verifies a self-dependency is a cycle.
func TestStructValidationDepsSelfCycle(t *testing.T) {
	v := New()

	_, err := v.RegisterStructValidationWithDeps(
		func(sl StructLevel) {},
		StructFieldDeps{Field: "Items", Reads: []string{"Items"}},
		order{},
	)
	NotEqual(t, err, nil)
	_, ok := err.(*ErrStructDepsCycle)
	Equal(t, ok, true)
}

// TestStructValidationDepsUnknownField verifies that a dependency on a field
// that does not exist is reported as an error.
func TestStructValidationDepsUnknownField(t *testing.T) {
	v := New()

	// unknown field in Field
	_, err := v.RegisterStructValidationWithDeps(
		func(sl StructLevel) {},
		StructFieldDeps{Field: "NonExistent", Reads: []string{"Items"}},
		order{},
	)
	NotEqual(t, err, nil)
	_, ok := err.(*ErrStructFieldNotFound)
	Equal(t, ok, true)

	// unknown field in Reads
	_, err = v.RegisterStructValidationWithDeps(
		func(sl StructLevel) {},
		StructFieldDeps{Field: "Subtotal", Reads: []string{"NonExistent"}},
		order{},
	)
	NotEqual(t, err, nil)
	_, ok = err.(*ErrStructFieldNotFound)
	Equal(t, ok, true)
}

// TestStructValidationDepsUnknownFieldBeforeCycle verifies that an unknown
// field is reported before a cycle, even when both would be triggered.
func TestStructValidationDepsUnknownFieldBeforeCycle(t *testing.T) {
	v := New()

	_, err := v.RegisterStructValidationWithDeps(
		func(sl StructLevel) {},
		StructFieldDeps{Field: "Subtotal", Reads: []string{"Items"}},
		order{},
	)
	Equal(t, err, nil)
	_, err = v.RegisterStructValidationWithDeps(
		func(sl StructLevel) {},
		StructFieldDeps{Field: "Total", Reads: []string{"Subtotal"}},
		order{},
	)
	Equal(t, err, nil)

	// unknown field + would-be cycle -> unknown field wins
	_, err = v.RegisterStructValidationWithDeps(
		func(sl StructLevel) {},
		StructFieldDeps{Field: "Items", Reads: []string{"Total", "NonExistent"}},
		order{},
	)
	NotEqual(t, err, nil)
	_, ok := err.(*ErrStructFieldNotFound)
	Equal(t, ok, true)
}

// TestStructValidationDepsDoesNotExecuteFn verifies that registering and
// checking dependencies never executes the user's validation function.
func TestStructValidationDepsDoesNotExecuteFn(t *testing.T) {
	v := New()

	called := 0
	fn := func(sl StructLevel) {
		called++
	}

	// valid registration
	_, err := v.RegisterStructValidationWithDeps(
		fn,
		StructFieldDeps{Field: "Subtotal", Reads: []string{"Items"}},
		order{},
	)
	Equal(t, err, nil)
	Equal(t, called, 0)

	// unknown field -> error, fn not called
	_, err = v.RegisterStructValidationWithDeps(
		fn,
		StructFieldDeps{Field: "Subtotal", Reads: []string{"NonExistent"}},
		order{},
	)
	NotEqual(t, err, nil)
	Equal(t, called, 0)

	// cycle -> error, fn not called
	_, err = v.RegisterStructValidationWithDeps(
		fn,
		StructFieldDeps{Field: "Total", Reads: []string{"Subtotal"}},
		order{},
	)
	Equal(t, err, nil)
	_, err = v.RegisterStructValidationWithDeps(
		fn,
		StructFieldDeps{Field: "Items", Reads: []string{"Total"}},
		order{},
	)
	NotEqual(t, err, nil)
	Equal(t, called, 0)
}

// TestStructValidationDepsNestedAlias verifies that nested struct paths and
// TagNameFunc aliases are resolved according to the validator's naming rules.
func TestStructValidationDepsNestedAlias(t *testing.T) {
	v := New()
	v.RegisterTagNameFunc(jsonTagNameFunc)

	plan, err := v.RegisterStructValidationWithDeps(
		func(sl StructLevel) {},
		StructFieldDeps{Field: "total", Reads: []string{"subtotal", "address.city"}},
		orderWithAddress{},
	)
	Equal(t, err, nil)
	NotEqual(t, plan, nil)

	// total depends on subtotal and address.city; both are prerequisites
	Equal(t, plan.Groups(), [][]string{{"Address.City", "Subtotal"}, {"Total"}})
}

// TestStructValidationDepsNestedAliasUnknown verifies that an unknown nested
// alias is reported.
func TestStructValidationDepsNestedAliasUnknown(t *testing.T) {
	v := New()
	v.RegisterTagNameFunc(jsonTagNameFunc)

	_, err := v.RegisterStructValidationWithDeps(
		func(sl StructLevel) {},
		StructFieldDeps{Field: "total", Reads: []string{"address.zip"}},
		orderWithAddress{},
	)
	NotEqual(t, err, nil)
	_, ok := err.(*ErrStructFieldNotFound)
	Equal(t, ok, true)
}

// TestStructValidationDepsFnExecuted verifies that a registered validation
// function is executed during validation (opt-in behaviour).
func TestStructValidationDepsFnExecuted(t *testing.T) {
	v := New()

	called := 0
	_, err := v.RegisterStructValidationWithDeps(
		func(sl StructLevel) {
			called++
		},
		StructFieldDeps{Field: "Subtotal", Reads: []string{"Items"}},
		order{},
	)
	Equal(t, err, nil)

	err = v.Struct(order{})
	Equal(t, err, nil)
	Equal(t, called, 1)
}

// TestStructValidationDepsOldRegistrationUnchanged verifies the old
// registration method still works without dependencies and that no plan is
// produced for it.
func TestStructValidationDepsOldRegistrationUnchanged(t *testing.T) {
	v := New()

	called := 0
	v.RegisterStructValidation(func(sl StructLevel) {
		called++
	}, order{})

	err := v.Struct(order{})
	Equal(t, err, nil)
	Equal(t, called, 1)

	// no plan for old registration
	_, err = v.StructValidationPlan(order{})
	NotEqual(t, err, nil)
}

// TestStructValidationDepsDiamond verifies a diamond dependency is grouped
// correctly.
func TestStructValidationDepsDiamond(t *testing.T) {
	type diamond struct {
		A string
		B string
		C string
		D string
	}

	v := New()

	// B and C depend on D; A depends on B and C
	_, err := v.RegisterStructValidationWithDeps(
		func(sl StructLevel) {},
		StructFieldDeps{Field: "B", Reads: []string{"D"}},
		diamond{},
	)
	Equal(t, err, nil)
	_, err = v.RegisterStructValidationWithDeps(
		func(sl StructLevel) {},
		StructFieldDeps{Field: "C", Reads: []string{"D"}},
		diamond{},
	)
	Equal(t, err, nil)
	plan, err := v.RegisterStructValidationWithDeps(
		func(sl StructLevel) {},
		StructFieldDeps{Field: "A", Reads: []string{"B", "C"}},
		diamond{},
	)
	Equal(t, err, nil)

	Equal(t, plan.Groups(), [][]string{{"D"}, {"B", "C"}, {"A"}})
}

// TestStructValidationDepsNoReads verifies a rule with no reads is a root.
func TestStructValidationDepsNoReads(t *testing.T) {
	v := New()

	plan, err := v.RegisterStructValidationWithDeps(
		func(sl StructLevel) {},
		StructFieldDeps{Field: "Items", Reads: nil},
		order{},
	)
	Equal(t, err, nil)
	Equal(t, plan.Groups(), [][]string{{"Items"}})
}

// TestStructValidationPlanGetter verifies the plan getter.
func TestStructValidationPlanGetter(t *testing.T) {
	v := New()

	_, err := v.RegisterStructValidationWithDeps(
		func(sl StructLevel) {},
		StructFieldDeps{Field: "Subtotal", Reads: []string{"Items"}},
		order{},
	)
	Equal(t, err, nil)

	plan, err := v.StructValidationPlan(order{})
	Equal(t, err, nil)
	Equal(t, plan.Groups(), [][]string{{"Items"}, {"Subtotal"}})
}
