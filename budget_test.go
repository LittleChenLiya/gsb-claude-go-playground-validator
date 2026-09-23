package validator

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	. "github.com/go-playground/assert/v2"
)

func budgetInt(i int) *int { return &i }

func TestStructWithBudgetMaxErrorsTruncation(t *testing.T) {
	type budgetList struct {
		Items []string `validate:"dive,required,counted"`
	}

	validate := New()
	var counted int
	err := validate.RegisterValidation("counted", func(fl FieldLevel) bool {
		counted++
		return true
	})
	Equal(t, err, nil)

	res, err := validate.StructWithBudget(context.Background(), budgetList{Items: []string{"", "", ""}}, StructBudget{
		MaxErrors: budgetInt(2),
	})
	Equal(t, err, nil)
	Equal(t, res.StopReason, BudgetStopMaxErrors)
	Equal(t, res.Truncated, true)
	Equal(t, len(res.Errors), 2)
	Equal(t, strings.HasSuffix(res.Errors[0].Namespace(), "Items[0]"), true)
	Equal(t, strings.HasSuffix(res.Errors[1].Namespace(), "Items[1]"), true)
	// the third item was never entered, so its custom callback never ran
	Equal(t, counted, 0)
	// charged visits: the Items field itself plus the first two elements
	Equal(t, res.NodesVisited, 3)
}

func TestStructWithBudgetContextCancel(t *testing.T) {
	type budgetCtx struct {
		A string `validate:"canceler"`
		B string `validate:"bprobe"`
	}

	validate := New()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// barrier: the callback cancels the context and blocks until the
	// cancellation is visible before returning, proving the current callback
	// is allowed to finish.
	var aCalls, bCalls int
	err := validate.RegisterValidation("canceler", func(fl FieldLevel) bool {
		aCalls++
		cancel()
		<-ctx.Done()
		return true
	})
	Equal(t, err, nil)
	err = validate.RegisterValidation("bprobe", func(fl FieldLevel) bool {
		bCalls++
		return true
	})
	Equal(t, err, nil)

	res, err := validate.StructWithBudget(ctx, budgetCtx{}, StructBudget{})
	Equal(t, err, nil)
	Equal(t, res.StopReason, BudgetStopContextCanceled)
	Equal(t, aCalls, 1)
	Equal(t, bCalls, 0) // next node was never entered
	Equal(t, res.NodesVisited, 1)
	Equal(t, len(res.Errors), 0)
	Equal(t, res.Truncated, false)
}

func TestStructWithBudgetCycle(t *testing.T) {
	type budgetNode struct {
		Name string `validate:"required"`
		Next *budgetNode
	}

	validate := New()

	self := &budgetNode{Name: "self"}
	self.Next = self

	res, err := validate.StructWithBudget(context.Background(), self, StructBudget{})
	Equal(t, err, nil)
	Equal(t, res.StopReason, BudgetStopCycle)
	Equal(t, len(res.Errors), 0)

	// indirect cycle A -> B -> A
	a := &budgetNode{Name: "a"}
	b := &budgetNode{Name: "b"}
	a.Next = b
	b.Next = a

	res, err = validate.StructWithBudget(context.Background(), a, StructBudget{})
	Equal(t, err, nil)
	Equal(t, res.StopReason, BudgetStopCycle)
}

func TestStructWithBudgetSharedSubStruct(t *testing.T) {
	type budgetInner struct {
		X string `validate:"required"`
	}
	type budgetOuter struct {
		A *budgetInner
		B *budgetInner
	}

	validate := New()
	shared := &budgetInner{}

	res, err := validate.StructWithBudget(context.Background(), budgetOuter{A: shared, B: shared}, StructBudget{})
	Equal(t, err, nil)
	// shared sub-object on different branches is not a cycle
	Equal(t, res.StopReason, BudgetStopNone)
	Equal(t, res.Truncated, false)
	// each branch charged separately: A, A.X, B, B.X
	Equal(t, res.NodesVisited, 4)
	Equal(t, len(res.Errors), 2)
	Equal(t, strings.HasSuffix(res.Errors[0].Namespace(), "A.X"), true)
	Equal(t, strings.HasSuffix(res.Errors[1].Namespace(), "B.X"), true)
}

func TestStructWithBudgetPoolReuse(t *testing.T) {
	type budgetReuse struct {
		A string `validate:"required"`
		B string `validate:"required"`
	}

	validate := New()
	budget := StructBudget{MaxErrors: budgetInt(1)}

	// first call leaves errors behind in the pooled state path
	res1, err := validate.StructWithBudget(context.Background(), budgetReuse{}, budget)
	Equal(t, err, nil)
	Equal(t, len(res1.Errors), 1)
	Equal(t, res1.Truncated, true)
	Equal(t, res1.StopReason, BudgetStopMaxErrors)

	// second call on a clean value must start from a clean state
	res2, err := validate.StructWithBudget(context.Background(), budgetReuse{A: "x", B: "y"}, budget)
	Equal(t, err, nil)
	Equal(t, res2.StopReason, BudgetStopNone)
	Equal(t, len(res2.Errors), 0)
	Equal(t, res2.Truncated, false)

	// third call on the failing value reproduces exactly the first result
	res3, err := validate.StructWithBudget(context.Background(), budgetReuse{}, budget)
	Equal(t, err, nil)
	Equal(t, len(res3.Errors), 1)
	Equal(t, res3.Errors[0].Namespace(), res1.Errors[0].Namespace())
	Equal(t, res3.Truncated, true)

	// legacy entry points are unaffected and also see a clean pool
	err = validate.Struct(budgetReuse{})
	Equal(t, err != nil, true)
	Equal(t, len(err.(ValidationErrors)), 2)
	err = validate.Struct(budgetReuse{A: "x", B: "y"})
	Equal(t, err, nil)
}

func TestStructWithBudgetMaxNodes(t *testing.T) {
	type budgetFields struct {
		A string `validate:"probe"`
		B string `validate:"probe"`
		C string `validate:"probe"`
	}

	validate := New()
	var probes, structLevelCalls int
	err := validate.RegisterValidation("probe", func(fl FieldLevel) bool {
		probes++
		return true
	})
	Equal(t, err, nil)
	validate.RegisterStructValidation(func(sl StructLevel) {
		structLevelCalls++
	}, budgetFields{})

	res, err := validate.StructWithBudget(context.Background(), budgetFields{}, StructBudget{
		MaxNodes: budgetInt(2),
	})
	Equal(t, err, nil)
	Equal(t, res.StopReason, BudgetStopMaxNodes)
	Equal(t, res.NodesVisited, 2)
	Equal(t, probes, 2)
	// past the boundary the struct-level callback must not run
	Equal(t, structLevelCalls, 0)

	// zero nodes: no field work at all
	probes, structLevelCalls = 0, 0
	res, err = validate.StructWithBudget(context.Background(), budgetFields{}, StructBudget{
		MaxNodes: budgetInt(0),
	})
	Equal(t, err, nil)
	Equal(t, res.StopReason, BudgetStopMaxNodes)
	Equal(t, res.NodesVisited, 0)
	Equal(t, probes, 0)
	Equal(t, structLevelCalls, 0)
}

func TestStructWithBudgetStructLevelRunsWithinBudget(t *testing.T) {
	type budgetSL struct {
		A string `validate:"required"`
	}

	validate := New()
	var calls int
	validate.RegisterStructValidation(func(sl StructLevel) {
		calls++
	}, budgetSL{})

	res, err := validate.StructWithBudget(context.Background(), budgetSL{A: "ok"}, StructBudget{})
	Equal(t, err, nil)
	Equal(t, res.StopReason, BudgetStopNone)
	Equal(t, calls, 1)
	Equal(t, len(res.Errors), 0)
}

func TestStructWithBudgetInvalidBudget(t *testing.T) {
	type budgetAny struct {
		A string
	}

	validate := New()

	res, err := validate.StructWithBudget(context.Background(), budgetAny{}, StructBudget{MaxNodes: budgetInt(-1)})
	Equal(t, errors.Is(err, ErrInvalidBudget), true)
	Equal(t, res.StopReason, BudgetStopInvalidInput)

	res, err = validate.StructWithBudget(context.Background(), budgetAny{}, StructBudget{MaxErrors: budgetInt(-2)})
	Equal(t, errors.Is(err, ErrInvalidBudget), true)
	Equal(t, res.StopReason, BudgetStopInvalidInput)

	// non-struct input mirrors StructCtx behavior
	res, err = validate.StructWithBudget(context.Background(), 42, StructBudget{})
	Equal(t, res.StopReason, BudgetStopInvalidInput)
	var invalid *InvalidValidationError
	Equal(t, errors.As(err, &invalid), true)
	Equal(t, invalid.Type == reflect.TypeOf(42), true)
}

func TestStructWithBudgetMaxErrorsZero(t *testing.T) {
	type budgetZero struct {
		A string `validate:"required"`
	}

	validate := New()
	res, err := validate.StructWithBudget(context.Background(), budgetZero{}, StructBudget{
		MaxErrors: budgetInt(0),
	})
	Equal(t, err, nil)
	Equal(t, res.StopReason, BudgetStopMaxErrors)
	Equal(t, res.Truncated, true)
	Equal(t, len(res.Errors), 0)
	Equal(t, res.NodesVisited, 0)
}

func TestStructWithBudgetMapSortedKeys(t *testing.T) {
	type budgetMap struct {
		M map[string]string `validate:"dive,keys,required,endkeys,required,recorder"`
	}

	validate := New()
	var mu sync.Mutex
	var visited []string
	err := validate.RegisterValidation("recorder", func(fl FieldLevel) bool {
		mu.Lock()
		visited = append(visited, fl.FieldName())
		mu.Unlock()
		return true
	})
	Equal(t, err, nil)

	m := map[string]string{"c": "x", "a": "x", "b": "x"}
	res, err := validate.StructWithBudget(context.Background(), budgetMap{M: m}, StructBudget{})
	Equal(t, err, nil)
	Equal(t, res.StopReason, BudgetStopNone)
	Equal(t, len(visited), 3)
	// deterministic sorted key order regardless of map iteration order
	Equal(t, visited[0], "M[a]")
	Equal(t, visited[1], "M[b]")
	Equal(t, visited[2], "M[c]")

	// a limited budget always lands on the same (sorted) prefix
	visited = nil
	res, err = validate.StructWithBudget(context.Background(), budgetMap{M: m}, StructBudget{
		MaxNodes: budgetInt(3),
	})
	Equal(t, err, nil)
	Equal(t, res.StopReason, BudgetStopMaxNodes)
	Equal(t, len(visited), 2)
	Equal(t, visited[0], "M[a]")
	Equal(t, visited[1], "M[b]")
}

func TestStructWithBudgetMapNonStringKey(t *testing.T) {
	type budgetBadMap struct {
		M map[int]string `validate:"dive,required"`
	}

	validate := New()
	res, err := validate.StructWithBudget(context.Background(), budgetBadMap{M: map[int]string{1: "x"}}, StructBudget{})
	Equal(t, errors.Is(err, ErrNonStringMapKey), true)
	Equal(t, res.StopReason, BudgetStopInvalidInput)
}

func TestStructWithBudgetAliasDiveCustom(t *testing.T) {
	type budgetAlias struct {
		Names []string `validate:"dive,notblankalias"`
	}

	validate := New()
	validate.RegisterAlias("notblankalias", "required,customtag")
	var custom int
	err := validate.RegisterValidation("customtag", func(fl FieldLevel) bool {
		custom++
		return fl.Field().String() != "bad"
	})
	Equal(t, err, nil)

	res, err := validate.StructWithBudget(context.Background(), budgetAlias{Names: []string{"ok", "bad", ""}}, StructBudget{})
	Equal(t, err, nil)
	Equal(t, res.StopReason, BudgetStopNone)
	Equal(t, len(res.Errors), 2)
	Equal(t, res.Errors[0].Tag(), "notblankalias")
	Equal(t, res.Errors[1].Tag(), "notblankalias")
	Equal(t, custom, 2) // skipped for the empty string which failed required first
}

func TestStructWithBudgetDoesNotMutateCaller(t *testing.T) {
	type budgetCaller struct {
		M map[string]string `validate:"dive,required"`
	}

	validate := New()
	in := budgetCaller{M: map[string]string{"b": "x", "a": "x"}}
	res, err := validate.StructWithBudget(context.Background(), in, StructBudget{})
	Equal(t, err, nil)
	Equal(t, res.StopReason, BudgetStopNone)
	Equal(t, len(in.M), 2)
	Equal(t, in.M["a"], "x")
	Equal(t, in.M["b"], "x")
}
