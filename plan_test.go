package validator

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"strings"
	"sync/atomic"
	"testing"

	. "github.com/go-playground/assert/v2"
)

// NOTES:
// - Run "go test -run TestCompileSchema -v ." and
//   "go test -run TestStructWithPlan -v ." to run these tests

type planInner struct {
	Retries int    `validate:"min=abc"`              // bad numeric param
	Confirm string `validate:"eqfield=DoesNotExist"` // dangling cross-field reference
	Code    string `validate:"planCheck"`            // registered custom validation, must never be invoked at compile time
}

type planOuter struct {
	Name  string    `validate:"required"`
	Inner planInner `validate:"required"`
}

func TestCompileSchemaAggregatesDiagnostics(t *testing.T) {
	v := New()

	var calls int64
	err := v.RegisterValidation("planCheck", func(fl FieldLevel) bool {
		atomic.AddInt64(&calls, 1)
		return true
	})
	Equal(t, err, nil)

	plan, err := v.CompileSchema(reflect.TypeOf(planOuter{}))
	if err == nil {
		t.Fatalf("expected schema errors, got a valid plan: %+v", plan)
	}
	if plan != nil {
		t.Fatalf("expected nil plan on schema errors, got %+v", plan)
	}

	serrs, ok := err.(SchemaErrors)
	if !ok {
		t.Fatalf("expected SchemaErrors, got %T", err)
	}
	Equal(t, len(serrs), 2)

	var badParam, badRef *SchemaError
	for _, se := range serrs {
		switch se.Kind {
		case SchemaErrInvalidParam:
			badParam = se
		case SchemaErrFieldRef:
			badRef = se
		default:
			t.Fatalf("unexpected diagnostic: %+v", se)
		}
	}

	if badParam == nil || badRef == nil {
		t.Fatalf("expected one invalid-param and one field-ref diagnostic, got %v", serrs)
	}

	// both diagnostics carry the full nested field path
	Equal(t, badParam.Path, "planOuter.Inner.Retries")
	Equal(t, badParam.Tag, "min")
	Equal(t, badParam.Param, "abc")
	Equal(t, badRef.Path, "planOuter.Inner.Confirm")
	Equal(t, badRef.Tag, "eqfield")
	Equal(t, badRef.Param, "DoesNotExist")

	// diagnostics mention the field paths in their messages too
	if !strings.Contains(badParam.Error(), "planOuter.Inner.Retries") {
		t.Fatalf("diagnostic message missing field path: %s", badParam.Error())
	}
	if !strings.Contains(badRef.Error(), "planOuter.Inner.Confirm") {
		t.Fatalf("diagnostic message missing field path: %s", badRef.Error())
	}

	// compile must not invoke any validation function
	Equal(t, atomic.LoadInt64(&calls), int64(0))
}

func TestCompileSchemaValidPlanAndReuse(t *testing.T) {
	type inner struct {
		Name     string `validate:"required"`
		Password string `validate:"eqfield=Name"`
	}
	type outer struct {
		OuterRef string   `validate:"required"`
		Email    string   `validate:"required,email"`
		Tags     []string `validate:"dive,min=2"`
		Inner    inner
	}

	v := New()
	plan, err := v.CompileSchema(reflect.TypeOf(outer{}))
	Equal(t, err, nil)
	if plan == nil {
		t.Fatal("expected a valid plan")
	}
	if plan.Type() != reflect.TypeOf(outer{}) {
		t.Fatalf("plan type mismatch: %v", plan.Type())
	}
	if plan.Fields() == 0 {
		t.Fatal("expected fields to have been inspected")
	}

	// reuse the same plan repeatedly
	for i := 0; i < 3; i++ {
		err = v.StructWithPlan(outer{OuterRef: "x", Email: "a@b.com", Tags: []string{"go", "cc"}, Inner: inner{Name: "n", Password: "n"}}, plan)
		Equal(t, err, nil)
	}
}

func TestStructWithPlanMatchesStructErrors(t *testing.T) {
	type user struct {
		Name     string `validate:"required"`
		Email    string `validate:"required,email"`
		Age      int    `validate:"gte=0,lt=130"`
		Password string `validate:"eqfield=Confirm"`
		Confirm  string
		Tags     []string          `validate:"dive,min=2"`
		Meta     map[string]string `validate:"dive,keys,min=2,endkeys,required"`
	}

	v := New()
	plan, err := v.CompileSchema(reflect.TypeOf(user{}))
	Equal(t, err, nil)

	bad := user{
		Email:    "not-an-email",
		Age:      200,
		Password: "a",
		Confirm:  "b",
		Tags:     []string{"ok", "x"},
		Meta:     map[string]string{"k": ""},
	}

	errPlan := v.StructWithPlan(bad, plan)
	errPlain := v.Struct(bad)

	if errPlan == nil || errPlain == nil {
		t.Fatalf("expected validation errors from both, plan=%v plain=%v", errPlan, errPlain)
	}

	summarize := func(err error) []string {
		ve := err.(ValidationErrors)
		out := make([]string, 0, len(ve))
		for _, fe := range ve {
			out = append(out, fe.Namespace()+"|"+fe.Tag())
		}
		sort.Strings(out)
		return out
	}

	Equal(t, summarize(errPlan), summarize(errPlain))
}

func TestCompileSchemaAliasCycle(t *testing.T) {
	v := New()
	v.RegisterAlias("aliasA", "aliasB")
	v.RegisterAlias("aliasB", "aliasA")

	type bad struct {
		S string `validate:"aliasA"`
	}

	plan, err := v.CompileSchema(reflect.TypeOf(bad{}))
	if plan != nil {
		t.Fatal("expected nil plan for alias cycle")
	}
	serrs, ok := err.(SchemaErrors)
	if !ok {
		t.Fatalf("expected SchemaErrors, got %T", err)
	}
	Equal(t, len(serrs), 1)
	Equal(t, serrs[0].Kind, SchemaErrAliasCycle)
	Equal(t, serrs[0].Path, "bad.S")

	// self-referencing alias
	v2 := New()
	v2.RegisterAlias("loop", "loop")
	type bad2 struct {
		S string `validate:"loop"`
	}
	_, err = v2.CompileSchema(reflect.TypeOf(bad2{}))
	serrs2, ok := err.(SchemaErrors)
	if !ok || len(serrs2) != 1 || serrs2[0].Kind != SchemaErrAliasCycle {
		t.Fatalf("expected a single alias-cycle diagnostic, got %v", err)
	}
}

func TestCompileSchemaRecursivePointer(t *testing.T) {
	type node struct {
		Name string `validate:"required"`
		Next *node
		Kids []*node `validate:"dive"`
	}

	v := New()
	plan, err := v.CompileSchema(reflect.TypeOf(node{}))
	Equal(t, err, nil)
	if plan == nil {
		t.Fatal("expected valid plan for recursive type")
	}

	// recursion terminates by type identity and the plan is usable
	err = v.StructWithPlan(node{Name: "root", Next: &node{Name: "leaf"}}, plan)
	Equal(t, err, nil)

	err = v.StructWithPlan(node{Name: "root", Next: &node{}}, plan)
	if err == nil {
		t.Fatal("expected validation error for missing nested Name")
	}
	Equal(t, len(err.(ValidationErrors)), 1)
}

func TestCompileSchemaDive(t *testing.T) {
	v := New()

	type good struct {
		Tags []string          `validate:"dive,min=2"`
		Meta map[string]string `validate:"dive,keys,min=2,endkeys,required"`
	}
	plan, err := v.CompileSchema(reflect.TypeOf(good{}))
	Equal(t, err, nil)
	if plan == nil {
		t.Fatal("expected valid plan for dive usage")
	}

	// dive on a non-slice/array/map field
	type badDive struct {
		N int `validate:"dive"`
	}
	_, err = v.CompileSchema(reflect.TypeOf(badDive{}))
	serrs, ok := err.(SchemaErrors)
	if !ok || len(serrs) != 1 || serrs[0].Kind != SchemaErrDive {
		t.Fatalf("expected a single dive diagnostic, got %v", err)
	}
	Equal(t, serrs[0].Path, "badDive.N")

	// endkeys without keys
	type badKeys struct {
		M map[string]string `validate:"dive,endkeys"`
	}
	_, err = v.CompileSchema(reflect.TypeOf(badKeys{}))
	serrs, ok = err.(SchemaErrors)
	if !ok || len(serrs) != 1 || serrs[0].Kind != SchemaErrDive {
		t.Fatalf("expected a single dive diagnostic, got %v", err)
	}

	// dive into struct elements: nested tags are compiled too
	type elem struct {
		Name string `validate:"required"`
	}
	type holder struct {
		Items []elem `validate:"dive"`
	}
	plan, err = v.CompileSchema(reflect.TypeOf(holder{}))
	Equal(t, err, nil)
	err = v.StructWithPlan(holder{Items: []elem{{Name: "a"}, {}}}, plan)
	if err == nil {
		t.Fatal("expected nested element validation error")
	}
	Equal(t, len(err.(ValidationErrors)), 1)
}

func TestCompileSchemaUnknownTag(t *testing.T) {
	v := New()
	type bad struct {
		S string `validate:"nosuchtag"`
	}
	_, err := v.CompileSchema(reflect.TypeOf(bad{}))
	serrs, ok := err.(SchemaErrors)
	if !ok || len(serrs) != 1 || serrs[0].Kind != SchemaErrUnknownTag {
		t.Fatalf("expected a single unknown-tag diagnostic, got %v", err)
	}
	Equal(t, serrs[0].Path, "bad.S")
	Equal(t, serrs[0].Tag, "nosuchtag")
}

func TestStructWithPlanRegistryChangeInvalidates(t *testing.T) {
	type doc struct {
		Name string `validate:"required"`
	}

	v := New()
	plan, err := v.CompileSchema(reflect.TypeOf(doc{}))
	Equal(t, err, nil)

	// plan works before the registry changes
	Equal(t, v.StructWithPlan(doc{Name: "x"}, plan), nil)

	// changing the registry invalidates the plan
	err = v.RegisterValidation("brandnew", func(fl FieldLevel) bool { return true })
	Equal(t, err, nil)

	err = v.StructWithPlan(doc{Name: "x"}, plan)
	var ipe *InvalidPlanError
	if !errors.As(err, &ipe) {
		t.Fatalf("expected *InvalidPlanError after registry change, got %v", err)
	}

	// alias registration also invalidates
	v2 := New()
	plan2, err := v2.CompileSchema(reflect.TypeOf(doc{}))
	Equal(t, err, nil)
	v2.RegisterAlias("somealias", "required")
	err = v2.StructWithPlan(doc{Name: "x"}, plan2)
	if !errors.As(err, &ipe) {
		t.Fatalf("expected *InvalidPlanError after alias registration, got %v", err)
	}

	// recompiling after the change yields a working plan again
	plan2, err = v2.CompileSchema(reflect.TypeOf(doc{}))
	Equal(t, err, nil)
	Equal(t, v2.StructWithPlan(doc{Name: "x"}, plan2), nil)

	// normal Struct behaviour is unaffected by any of this
	Equal(t, v.Struct(doc{Name: "x"}), nil)
	if err = v.Struct(doc{}); err == nil {
		t.Fatal("expected normal Struct validation error")
	}
}

func TestStructWithPlanInvalidUsage(t *testing.T) {
	type doc struct {
		Name string `validate:"required"`
	}
	type other struct {
		Name string `validate:"required"`
	}

	v := New()
	plan, err := v.CompileSchema(reflect.TypeOf(doc{}))
	Equal(t, err, nil)

	// nil plan
	var ipe *InvalidPlanError
	if err = v.StructWithPlan(doc{}, nil); !errors.As(err, &ipe) {
		t.Fatalf("expected *InvalidPlanError for nil plan, got %v", err)
	}

	// plan from another instance
	v2 := New()
	plan2, err := v2.CompileSchema(reflect.TypeOf(doc{}))
	Equal(t, err, nil)
	if err = v.StructWithPlan(doc{}, plan2); !errors.As(err, &ipe) {
		t.Fatalf("expected *InvalidPlanError for foreign plan, got %v", err)
	}

	// wrong value type
	err = v.StructWithPlan(other{}, plan)
	var ive *InvalidValidationError
	if !errors.As(err, &ive) {
		t.Fatalf("expected *InvalidValidationError for mismatched type, got %v", err)
	}

	// pointer to the planned type is accepted
	Equal(t, v.StructWithPlan(&doc{Name: "x"}, plan), nil)

	// context variant honours the same checks
	Equal(t, v.StructWithPlanCtx(context.Background(), doc{Name: "x"}, plan), nil)
	if err = v.StructWithPlanCtx(context.Background(), doc{}, nil); !errors.As(err, &ipe) {
		t.Fatalf("expected *InvalidPlanError for nil plan (ctx), got %v", err)
	}
}

func TestCompileSchemaNonStruct(t *testing.T) {
	v := New()
	var ive *InvalidValidationError

	_, err := v.CompileSchema(reflect.TypeOf(42))
	if !errors.As(err, &ive) {
		t.Fatalf("expected *InvalidValidationError for non-struct, got %v", err)
	}

	_, err = v.CompileSchema(nil)
	if !errors.As(err, &ive) {
		t.Fatalf("expected *InvalidValidationError for nil type, got %v", err)
	}

	// pointer to struct is accepted and dereferenced
	type doc struct {
		Name string `validate:"required"`
	}
	plan, err := v.CompileSchema(reflect.TypeOf(&doc{}))
	Equal(t, err, nil)
	if plan.Type() != reflect.TypeOf(doc{}) {
		t.Fatalf("plan type mismatch: %v", plan.Type())
	}
}

func TestCompileSchemaCrossStructField(t *testing.T) {
	type cfg struct {
		Min int `validate:"required"`
		Max int `validate:"gtcsfield=Min"`
	}
	v := New()
	plan, err := v.CompileSchema(reflect.TypeOf(cfg{}))
	Equal(t, err, nil)
	Equal(t, v.StructWithPlan(cfg{Min: 1, Max: 2}, plan), nil)

	type badCfg struct {
		Min int `validate:"required"`
		Max int `validate:"eqcsfield=Nope"`
	}
	_, err = v.CompileSchema(reflect.TypeOf(badCfg{}))
	serrs, ok := err.(SchemaErrors)
	if !ok || len(serrs) != 1 || serrs[0].Kind != SchemaErrFieldRef {
		t.Fatalf("expected a single field-ref diagnostic, got %v", err)
	}
	Equal(t, serrs[0].Path, "badCfg.Max")

	// dotted references through nested structs resolve
	type inner struct {
		Limit int
	}
	type outer struct {
		Inner inner
		Val   int `validate:"eqcsfield=Inner.Limit"`
	}
	_, err = v.CompileSchema(reflect.TypeOf(outer{}))
	Equal(t, err, nil)
}
