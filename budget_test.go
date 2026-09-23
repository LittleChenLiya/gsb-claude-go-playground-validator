package validator

import (
	"context"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func TestStructWithBudgetMaxErrors(t *testing.T) {
	v := New()
	v.RegisterAlias("budgetreq", "required")

	var mu sync.Mutex
	var calls int
	err := v.RegisterValidation("budgetcount", func(fl FieldLevel) bool {
		mu.Lock()
		calls++
		mu.Unlock()
		return true
	})
	if err != nil {
		t.Fatal(err)
	}

	type form struct {
		Items []string `validate:"dive,budgetcount,budgetreq"`
	}

	errs, res, err := v.StructWithBudget(context.Background(), form{Items: []string{"", "", ""}}, Budget{MaxNodes: 100, MaxErrors: 2})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(errs) != 2 {
		t.Fatalf("expected exactly 2 errors, got %d: %v", len(errs), errs)
	}
	if errs[0].Namespace() != "form.Items[0]" || errs[1].Namespace() != "form.Items[1]" {
		t.Fatalf("expected first two item errors, got %s and %s", errs[0].Namespace(), errs[1].Namespace())
	}
	if !res.Truncated {
		t.Fatal("expected truncated result")
	}
	if res.Reason != StopMaxErrors {
		t.Fatalf("expected StopMaxErrors, got %s", res.Reason)
	}
	if calls != 2 {
		t.Fatalf("expected custom callback for the third item to never run (calls=2), got %d", calls)
	}
}

func TestStructWithBudgetContextCancel(t *testing.T) {
	v := New()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var calls int
	err := v.RegisterValidationCtx("budgetcancel", func(ctx context.Context, fl FieldLevel) bool {
		calls++
		// cancel from inside the callback; the current callback is allowed
		// to finish, but no further node may be entered.
		cancel()
		return true
	})
	if err != nil {
		t.Fatal(err)
	}

	type doc struct {
		A string `validate:"budgetcancel"`
		B string `validate:"budgetcancel"`
		C string `validate:"budgetcancel"`
	}

	errs, res, err := v.StructWithBudget(ctx, doc{}, Budget{MaxNodes: 100, MaxErrors: 100})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(errs) != 0 {
		t.Fatalf("expected no errors, got %v", errs)
	}
	if res.Reason != StopContextCanceled {
		t.Fatalf("expected StopContextCanceled, got %s", res.Reason)
	}
	if calls != 1 {
		t.Fatalf("expected only the first callback to run, got %d", calls)
	}
}

func TestStructWithBudgetMaxNodes(t *testing.T) {
	v := New()

	type inner struct {
		Name string `validate:"required"`
	}
	type outer struct {
		A inner
		B inner
	}

	// root struct is not counted; each field visit is: A, A.Name hits the cap.
	errs, res, err := v.StructWithBudget(context.Background(), outer{}, Budget{MaxNodes: 2, MaxErrors: 100})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Reason != StopMaxNodes {
		t.Fatalf("expected StopMaxNodes, got %s", res.Reason)
	}
	if res.Nodes != 2 {
		t.Fatalf("expected 2 visited nodes, got %d", res.Nodes)
	}
	if len(errs) != 1 || errs[0].Namespace() != "outer.A.Name" {
		t.Fatalf("expected only outer.A.Name error, got %v", errs)
	}

	// zero node budget: no field may be entered at all.
	errs, res, err = v.StructWithBudget(context.Background(), outer{}, Budget{MaxNodes: 0, MaxErrors: 100})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Reason != StopMaxNodes || res.Nodes != 0 || len(errs) != 0 {
		t.Fatalf("expected immediate StopMaxNodes with no visits, got %+v errs=%v", res, errs)
	}

	// zero error budget: no error may be collected.
	_, res, err = v.StructWithBudget(context.Background(), outer{}, Budget{MaxNodes: 100, MaxErrors: 0})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Reason != StopMaxErrors || !res.Truncated {
		t.Fatalf("expected immediate StopMaxErrors truncation, got %+v", res)
	}
}

func TestStructWithBudgetNegativeRejected(t *testing.T) {
	v := New()

	if _, _, err := v.StructWithBudget(context.Background(), struct{}{}, Budget{MaxNodes: -1}); err == nil {
		t.Fatal("expected error for negative MaxNodes")
	}
	if _, _, err := v.StructWithBudget(context.Background(), struct{}{}, Budget{MaxErrors: -1}); err == nil {
		t.Fatal("expected error for negative MaxErrors")
	}
	if _, _, err := v.StructWithBudget(context.Background(), 42, Budget{MaxNodes: 1, MaxErrors: 1}); err == nil {
		t.Fatal("expected InvalidValidationError for non-struct input")
	}
}

func TestStructWithBudgetCycle(t *testing.T) {
	v := New()

	type node struct {
		Val  int `validate:"gte=0"`
		Next *node
	}
	n := &node{Val: 1}
	n.Next = n

	_, res, err := v.StructWithBudget(context.Background(), n, Budget{MaxNodes: 1000, MaxErrors: 1000})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Reason != StopCycle {
		t.Fatalf("expected StopCycle, got %s", res.Reason)
	}
	if !strings.Contains(res.Path, "Next") {
		t.Fatalf("expected cycle path to mention Next, got %q", res.Path)
	}
}

func TestStructWithBudgetSharedSubStruct(t *testing.T) {
	v := New()

	type shared struct {
		Name string `validate:"required"`
	}
	type holder struct {
		A *shared
		B *shared
	}

	s := &shared{Name: "ok"}
	errs, res, err := v.StructWithBudget(context.Background(), holder{A: s, B: s}, Budget{MaxNodes: 100, MaxErrors: 100})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Reason != StopNone {
		t.Fatalf("shared sub-object in different branches must not be a cycle, got %s", res.Reason)
	}
	if len(errs) != 0 {
		t.Fatalf("expected no errors, got %v", errs)
	}
}

func TestStructWithBudgetPoolReuse(t *testing.T) {
	v := New()

	type form struct {
		Items []string `validate:"dive,required"`
	}
	in := form{Items: []string{"", "", ""}}

	// first call truncates early; the pooled state must be reset afterwards.
	errs1, res1, err := v.StructWithBudget(context.Background(), in, Budget{MaxNodes: 100, MaxErrors: 1})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(errs1) != 1 || res1.Reason != StopMaxErrors {
		t.Fatalf("unexpected first result: errs=%v res=%+v", errs1, res1)
	}

	// second call on the same instance must start from a clean state.
	errs2, res2, err := v.StructWithBudget(context.Background(), in, Budget{MaxNodes: 100, MaxErrors: 1})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(errs2) != 1 || errs2[0].Namespace() != errs1[0].Namespace() || res2.Reason != StopMaxErrors {
		t.Fatalf("pooled state leaked into second call: errs=%v res=%+v", errs2, res2)
	}

	// regular Struct must be unaffected and still see all three failures.
	err = v.Struct(in)
	ves, ok := err.(ValidationErrors)
	if !ok || len(ves) != 3 {
		t.Fatalf("expected Struct to report all 3 errors, got %v", err)
	}

	// the caller's object must not be modified.
	if !reflect.DeepEqual(in, form{Items: []string{"", "", ""}}) {
		t.Fatalf("input object was modified: %+v", in)
	}
}

func TestStructWithBudgetMapDeterministic(t *testing.T) {
	v := New()

	var order []string
	err := v.RegisterValidation("budgetorder", func(fl FieldLevel) bool {
		order = append(order, fl.FieldName())
		return true
	})
	if err != nil {
		t.Fatal(err)
	}

	type bag struct {
		M map[string]string `validate:"dive,budgetorder"`
	}
	in := bag{M: map[string]string{"c": "1", "a": "2", "b": "3"}}

	for i := 0; i < 5; i++ {
		order = nil
		_, res, err := v.StructWithBudget(context.Background(), in, Budget{MaxNodes: 100, MaxErrors: 100})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if res.Reason != StopNone {
			t.Fatalf("expected StopNone, got %s", res.Reason)
		}
		want := []string{"M[a]", "M[b]", "M[c]"}
		if !reflect.DeepEqual(order, want) {
			t.Fatalf("run %d: expected sorted key order %v, got %v", i, want, order)
		}
	}

	// a small node budget must deterministically land on the same keys.
	_, res, err := v.StructWithBudget(context.Background(), in, Budget{MaxNodes: 2, MaxErrors: 100})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Reason != StopMaxNodes || res.Nodes != 2 {
		t.Fatalf("expected StopMaxNodes after 2 nodes, got %+v", res)
	}
}

func TestStructWithBudgetMapNonStringKeyRejected(t *testing.T) {
	v := New()

	type bag struct {
		M map[int]string `validate:"dive,required"`
	}

	_, res, err := v.StructWithBudget(context.Background(), bag{M: map[int]string{1: "x"}}, Budget{MaxNodes: 100, MaxErrors: 100})
	if err == nil {
		t.Fatal("expected error for non-string map keys")
	}
	if res.Reason != StopError {
		t.Fatalf("expected StopError, got %s", res.Reason)
	}
}
