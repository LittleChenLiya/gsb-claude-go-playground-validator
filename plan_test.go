package validator

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	. "github.com/go-playground/assert/v2"
)

// types used by the plan tests

type planInner struct {
	Name  string `validate:"eqfield=DoesNotExist"`
	Count int    `validate:"gt=notanumber"`
	Code  string `validate:"planprobe"`
}

type planOuter struct {
	Inner planInner
}

type planNode struct {
	Value string `validate:"required"`
	Next  *planNode
	Kids  []planNode `validate:"dive"`
}

type planDiveBadInner struct {
	S string `validate:"unknown_tag_xyz"`
}

type planDiveBadOuter struct {
	Items []planDiveBadInner `validate:"dive"`
}

type planDiveWrongKind struct {
	N int `validate:"dive"`
}

type planCompareInner struct {
	Email string `validate:"required,email"`
}

type planCompare struct {
	Name     string   `validate:"required"`
	Age      int      `validate:"gte=0,lte=130"`
	Password string   `validate:"required"`
	Confirm  string   `validate:"eqfield=Password"`
	Tags     []string `validate:"dive,required"`
	Inner    planCompareInner
}

func planErrorSet(err error) map[string]string {
	m := make(map[string]string)
	if err == nil {
		return m
	}
	for _, fe := range err.(ValidationErrors) {
		m[fe.Namespace()] = fe.Tag()
	}
	return m
}

func TestCompileSchemaAggregatesDiagnostics(t *testing.T) {
	v := New()

	var calls int32
	err := v.RegisterValidation("planprobe", func(fl FieldLevel) bool {
		atomic.AddInt32(&calls, 1)
		return true
	})
	Equal(t, err, nil)

	plan, err := v.CompileSchema(reflect.TypeOf(planOuter{}))
	if plan != nil {
		t.Fatalf("expected no plan, got %v", plan)
	}
	if err == nil {
		t.Fatal("expected diagnostics error, got nil")
	}

	diags, ok := err.(PlanErrors)
	if !ok {
		t.Fatalf("expected PlanErrors, got %T", err)
	}

	// both problems of the nested struct are reported at once, with field paths
	Equal(t, len(diags), 2)
	Equal(t, diags[0].Path, "planOuter.Inner.Name")
	Equal(t, diags[0].Tag, "eqfield")
	Equal(t, diags[0].Param, "DoesNotExist")
	Equal(t, diags[1].Path, "planOuter.Inner.Count")
	Equal(t, diags[1].Tag, "gt")
	Equal(t, diags[1].Param, "notanumber")

	// diagnostics carry the field path in their message
	if !strings.Contains(diags[0].Error(), "planOuter.Inner.Name") {
		t.Fatalf("diagnostic missing field path: %s", diags[0].Error())
	}
	if !strings.Contains(err.Error(), "planOuter.Inner.Count") {
		t.Fatalf("aggregated error missing field path: %s", err.Error())
	}

	// compilation must not invoke any validation function
	Equal(t, atomic.LoadInt32(&calls), int32(0))
}

func TestCompileSchemaAliasCycle(t *testing.T) {
	v := New()
	v.RegisterAlias("cyclea", "cycleb")
	v.RegisterAlias("cycleb", "cyclea")
	v.RegisterAlias("cycleself", "cycleself")

	type T struct {
		A string `validate:"cyclea"`
		B string `validate:"cycleself"`
	}

	plan, err := v.CompileSchema(reflect.TypeOf(T{}))
	if plan != nil {
		t.Fatalf("expected no plan, got %v", plan)
	}

	diags, ok := err.(PlanErrors)
	if !ok {
		t.Fatalf("expected PlanErrors, got %T", err)
	}
	Equal(t, len(diags), 2)
	if !strings.Contains(diags[0].Message, "alias cycle detected: cyclea -> cycleb -> cyclea") {
		t.Fatalf("unexpected cycle message: %s", diags[0].Message)
	}
	if !strings.Contains(diags[1].Message, "alias cycle detected: cycleself -> cycleself") {
		t.Fatalf("unexpected cycle message: %s", diags[1].Message)
	}
}

func TestCompileSchemaRecursiveType(t *testing.T) {
	v := New()

	// recursive pointer + recursive dive slice terminate by type identity
	plan, err := v.CompileSchema(reflect.TypeOf(planNode{}))
	Equal(t, err, nil)
	if plan == nil {
		t.Fatal("expected plan, got nil")
	}
	Equal(t, plan.Type(), reflect.TypeOf(planNode{}))

	n := planNode{Next: &planNode{}, Kids: []planNode{{}}}

	errPlan := v.StructWithPlan(n, plan)
	errStruct := v.Struct(n)
	if errPlan == nil || errStruct == nil {
		t.Fatal("expected validation errors")
	}
	Equal(t, planErrorSet(errPlan), planErrorSet(errStruct))
	Equal(t, len(planErrorSet(errPlan)), 3)
}

func TestCompileSchemaDive(t *testing.T) {
	v := New()

	// unknown tag inside a dived-into element type is reported with the field path
	plan, err := v.CompileSchema(reflect.TypeOf(planDiveBadOuter{}))
	if plan != nil {
		t.Fatalf("expected no plan, got %v", plan)
	}
	diags, ok := err.(PlanErrors)
	if !ok {
		t.Fatalf("expected PlanErrors, got %T", err)
	}
	Equal(t, len(diags), 1)
	Equal(t, diags[0].Path, "planDiveBadOuter.Items.S")
	Equal(t, diags[0].Tag, "unknown_tag_xyz")

	// dive on a non slice/map is a compile time error instead of a runtime panic
	plan, err = v.CompileSchema(reflect.TypeOf(planDiveWrongKind{}))
	if plan != nil {
		t.Fatalf("expected no plan, got %v", plan)
	}
	diags, ok = err.(PlanErrors)
	if !ok {
		t.Fatalf("expected PlanErrors, got %T", err)
	}
	Equal(t, len(diags), 1)
	Equal(t, diags[0].Tag, "dive")
}

func TestStructWithPlanMatchesStruct(t *testing.T) {
	v := New()

	plan, err := v.CompileSchema(reflect.TypeOf(planCompare{}))
	Equal(t, err, nil)
	if plan == nil {
		t.Fatal("expected plan, got nil")
	}

	bad := planCompare{
		Age:     200,
		Confirm: "nope",
		Tags:    []string{"ok", ""},
		Inner:   planCompareInner{Email: "not-an-email"},
	}

	// a valid plan produces exactly the same error set as a regular Struct call
	errPlan := v.StructWithPlan(bad, plan)
	errStruct := v.Struct(bad)
	if errPlan == nil || errStruct == nil {
		t.Fatal("expected validation errors")
	}
	Equal(t, planErrorSet(errPlan), planErrorSet(errStruct))

	// reusing the reused plan keeps producing the same result
	errPlanAgain := v.StructWithPlan(&bad, plan)
	Equal(t, planErrorSet(errPlanAgain), planErrorSet(errPlan))

	good := planCompare{
		Name:     "gopher",
		Age:      30,
		Password: "secret",
		Confirm:  "secret",
		Tags:     []string{"a", "b"},
		Inner:    planCompareInner{Email: "gopher@example.com"},
	}
	Equal(t, v.StructWithPlan(good, plan), nil)
	Equal(t, v.Struct(good), nil)
}

func TestStructWithPlanStaleRegistry(t *testing.T) {
	v := New()

	plan, err := v.CompileSchema(reflect.TypeOf(planCompare{}))
	Equal(t, err, nil)

	good := planCompare{
		Name:     "gopher",
		Password: "secret",
		Confirm:  "secret",
		Inner:    planCompareInner{Email: "gopher@example.com"},
	}
	Equal(t, v.StructWithPlan(good, plan), nil)

	// changing the registry invalidates previously compiled plans
	Equal(t, v.RegisterValidation("brandnewtag", func(fl FieldLevel) bool { return true }), nil)

	err = v.StructWithPlan(good, plan)
	var stale *StalePlanError
	if !errors.As(err, &stale) {
		t.Fatalf("expected *StalePlanError, got %T (%v)", err, err)
	}
	Equal(t, stale.CompiledRegistryVersion, plan.RegistryVersion())

	// registering an alias also invalidates plans
	plan2, err := v.CompileSchema(reflect.TypeOf(planCompare{}))
	Equal(t, err, nil)
	v.RegisterAlias("brandnewalias", "required")
	err = v.StructWithPlan(good, plan2)
	if !errors.As(err, &stale) {
		t.Fatalf("expected *StalePlanError, got %T (%v)", err, err)
	}

	// the regular Struct path is unaffected by registry changes and plans
	Equal(t, v.Struct(good), nil)
	err = v.Struct(planCompare{})
	if err == nil {
		t.Fatal("expected validation errors from Struct")
	}
}

func TestStructWithPlanErrors(t *testing.T) {
	v1 := New()
	v2 := New()

	plan, err := v1.CompileSchema(reflect.TypeOf(planCompare{}))
	Equal(t, err, nil)

	// nil plan
	Equal(t, v1.StructWithPlan(planCompare{}, nil), ErrNilPlan)

	// plan compiled by another *Validate instance
	Equal(t, v2.StructWithPlan(planCompare{}, plan), ErrPlanValidatorMismatch)

	// value of a different type than the plan was compiled for
	err = v1.StructWithPlan("not a planCompare", plan)
	var invalid *InvalidValidationError
	if !errors.As(err, &invalid) {
		t.Fatalf("expected *InvalidValidationError, got %T (%v)", err, err)
	}

	err = v1.StructWithPlan(planNode{}, plan)
	if !errors.As(err, &invalid) {
		t.Fatalf("expected *InvalidValidationError, got %T (%v)", err, err)
	}

	// compiling a non struct type
	_, err = v1.CompileSchema(reflect.TypeOf(5))
	if !errors.As(err, &invalid) {
		t.Fatalf("expected *InvalidValidationError, got %T (%v)", err, err)
	}

	// compiling nil
	_, err = v1.CompileSchema(nil)
	if !errors.As(err, &invalid) {
		t.Fatalf("expected *InvalidValidationError, got %T (%v)", err, err)
	}

	// pointer to struct type is accepted and dereferenced
	planPtr, err := v1.CompileSchema(reflect.TypeOf(&planCompare{}))
	Equal(t, err, nil)
	Equal(t, planPtr.Type(), reflect.TypeOf(planCompare{}))
}

func TestCompileSchemaCrossFieldReferences(t *testing.T) {
	v := New()

	type NestedRefs struct {
		A string `validate:"eqcsfield=Root.Missing"`
		B string `validate:"required_with=Missing"`
		C string `validate:"required_if=Missing x"`
	}
	type Root struct {
		N NestedRefs
	}

	plan, err := v.CompileSchema(reflect.TypeOf(Root{}))
	if plan != nil {
		t.Fatalf("expected no plan, got %v", plan)
	}
	diags, ok := err.(PlanErrors)
	if !ok {
		t.Fatalf("expected PlanErrors, got %T", err)
	}
	Equal(t, len(diags), 3)
	Equal(t, diags[0].Path, "Root.N.A")
	Equal(t, diags[1].Path, "Root.N.B")
	Equal(t, diags[2].Path, "Root.N.C")

	// valid references compile cleanly, including nested paths
	type OKInner struct {
		A string `validate:"eqfield=B"`
		B string
	}
	type OKOuter struct {
		I OKInner
		C string `validate:"eqcsfield=I.B"`
	}
	planOK, err := v.CompileSchema(reflect.TypeOf(OKOuter{}))
	Equal(t, err, nil)
	if planOK == nil {
		t.Fatal("expected plan, got nil")
	}
}

func ExampleValidate_CompileSchema() {
	type Signup struct {
		Name     string `validate:"required"`
		Password string `validate:"required"`
		Confirm  string `validate:"eqfield=Password"`
	}

	type Broken struct {
		A int    `validate:"gt=abc"`
		B string `validate:"eqfield=Missing"`
	}

	validate := New()

	// precompilation rejects invalid schemas, reporting every problem at once
	_, err := validate.CompileSchema(reflect.TypeOf(Broken{}))
	fmt.Println(err)

	// a valid schema compiles into an immutable, reusable plan
	plan, err := validate.CompileSchema(reflect.TypeOf(Signup{}))
	if err != nil {
		panic(err)
	}

	err = validate.StructWithPlan(Signup{Name: "gopher", Password: "secret", Confirm: "typo"}, plan)
	fmt.Println(len(err.(ValidationErrors)), "validation error(s)")

	// Output:
	// validator: schema compilation failed with 2 problem(s):
	//   - Broken.A: tag 'gt' param 'abc': invalid parameter 'abc' for tag 'gt' on field of kind 'int'
	//   - Broken.B: tag 'eqfield' param 'Missing': cross-field reference 'Missing' not found in struct 'Broken'
	// 1 validation error(s)
}

func TestAliasCycleRuntimePanic(t *testing.T) {
	// the runtime parser now reports alias cycles with a descriptive panic
	// instead of recursing until the stack overflows
	v := New()
	v.RegisterAlias("runtimecyclea", "runtimecycleb")
	v.RegisterAlias("runtimecycleb", "runtimecyclea")

	type T struct {
		F string `validate:"runtimecyclea"`
	}

	PanicMatches(t, func() { _ = v.Struct(T{}) }, "Alias cycle detected: runtimecyclea -> runtimecycleb -> runtimecyclea")
}
