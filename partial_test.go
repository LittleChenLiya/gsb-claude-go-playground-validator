package validator

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"testing"

	. "github.com/go-playground/assert/v2"
)

func TestPartialDependentStartEnd(t *testing.T) {
	type DateRange struct {
		Start int    `validate:"gte=0"`
		End   int    `validate:"gtfield=Start"`
		Name  string `validate:"required"`
	}

	validate := New()

	// only Start changed, pushing it past End: End must be reported even
	// though it was not modified itself.
	res, err := validate.StructPartialDependent(DateRange{Start: 10, End: 5, Name: "ok"}, "Start")
	if res.RequiresFullValidation {
		t.Fatal("expected partial validation to be possible")
	}
	if err == nil {
		t.Fatal("expected validation errors")
	}
	verrs, ok := err.(ValidationErrors)
	if !ok {
		t.Fatalf("expected ValidationErrors, got %T", err)
	}
	if len(verrs) != 1 {
		t.Fatalf("expected 1 error, got %d: %v", len(verrs), verrs)
	}
	Equal(t, verrs[0].Field(), "End")
	Equal(t, verrs[0].Tag(), "gtfield")

	// the unrelated required rule on Name must not run during partial
	// validation, while full validation reports it.
	res, err = validate.StructPartialDependent(DateRange{Start: 1, End: 5}, "Start")
	if res.RequiresFullValidation {
		t.Fatal("expected partial validation to be possible")
	}
	Equal(t, err, nil)
	Equal(t, res.ValidatedFields, []string{"End", "Start"})

	err = validate.Struct(DateRange{Start: 1, End: 5})
	if err == nil {
		t.Fatal("expected full validation to report the missing Name")
	}
	Equal(t, len(err.(ValidationErrors)), 1)
	Equal(t, err.(ValidationErrors)[0].Field(), "Name")
}

func TestPartialDependentChained(t *testing.T) {
	type Chain struct {
		A int
		B int `validate:"gtfield=A"`
		C int `validate:"gtfield=B"`
	}

	validate := New()

	res, err := validate.StructPartialDependent(Chain{A: 5, B: 3, C: 2}, "A")
	if res.RequiresFullValidation {
		t.Fatal("expected partial validation to be possible")
	}
	if err == nil {
		t.Fatal("expected validation errors")
	}
	verrs := err.(ValidationErrors)
	if len(verrs) != 2 {
		t.Fatalf("expected 2 errors, got %d: %v", len(verrs), verrs)
	}
	fields := []string{verrs[0].Field(), verrs[1].Field()}
	sort.Strings(fields)
	Equal(t, fields, []string{"B", "C"})
	Equal(t, res.ValidatedFields, []string{"A", "B", "C"})

	// changing B must not revalidate A, but must revalidate C.
	res, err = validate.StructPartialDependent(Chain{A: 1, B: 5, C: 2}, "B")
	if res.RequiresFullValidation {
		t.Fatal("expected partial validation to be possible")
	}
	if err == nil {
		t.Fatal("expected validation errors")
	}
	verrs = err.(ValidationErrors)
	Equal(t, len(verrs), 1)
	Equal(t, verrs[0].Field(), "C")
	Equal(t, res.ValidatedFields, []string{"B", "C"})
}

func TestPartialDependentCycle(t *testing.T) {
	type Cyclic struct {
		A int `validate:"countme,gtefield=B"`
		B int `validate:"countme,gtefield=A"`
		C int `validate:"countme,gtefield=A"`
	}

	var mu sync.Mutex
	counts := map[string]int{}

	validate := New()
	err := validate.RegisterValidation("countme", func(fl FieldLevel) bool {
		mu.Lock()
		counts[fl.StructFieldName()]++
		mu.Unlock()
		return true
	})
	Equal(t, err, nil)

	res, err := validate.StructPartialDependent(Cyclic{A: 2, B: 2, C: 3}, "A")
	if res.RequiresFullValidation {
		t.Fatal("expected partial validation to be possible")
	}
	Equal(t, err, nil)

	// A, B and C form a dependency cycle through A; every affected field must
	// be validated exactly once, the cycle must not loop forever.
	Equal(t, counts, map[string]int{"A": 1, "B": 1, "C": 1})
	Equal(t, res.ValidatedFields, []string{"A", "B", "C"})
}

func TestPartialDependentNested(t *testing.T) {
	type Inner struct {
		Start int
		End   int `validate:"gtfield=Start"`
	}
	type Outer struct {
		Inner Inner
		Other string `validate:"required"`
	}

	validate := New()

	res, err := validate.StructPartialDependent(Outer{Inner: Inner{Start: 10, End: 5}}, "Inner.Start")
	if res.RequiresFullValidation {
		t.Fatal("expected partial validation to be possible")
	}
	if err == nil {
		t.Fatal("expected validation errors")
	}
	verrs := err.(ValidationErrors)
	Equal(t, len(verrs), 1)
	Equal(t, verrs[0].Field(), "End")
	Equal(t, verrs[0].StructNamespace(), "Outer.Inner.End")
	Equal(t, res.ValidatedFields, []string{"Inner.End", "Inner.Start"})

	// the unrelated required rule on Other must not run.
	res, err = validate.StructPartialDependent(Outer{Inner: Inner{Start: 1, End: 5}}, "Inner.Start")
	if res.RequiresFullValidation {
		t.Fatal("expected partial validation to be possible")
	}
	Equal(t, err, nil)

	// changing the whole nested struct validates it entirely.
	res, err = validate.StructPartialDependent(Outer{Inner: Inner{Start: 10, End: 5}}, "Inner")
	if res.RequiresFullValidation {
		t.Fatal("expected partial validation to be possible")
	}
	if err == nil {
		t.Fatal("expected validation errors")
	}
	verrs = err.(ValidationErrors)
	Equal(t, len(verrs), 1)
	Equal(t, verrs[0].Field(), "End")
	Equal(t, res.ValidatedFields, []string{"Inner"})
}

func TestPartialDependentUnknownPath(t *testing.T) {
	type Simple struct {
		Name string `validate:"required"`
	}

	validate := New()

	res, err := validate.StructPartialDependent(Simple{Name: "ok"}, "Nope")
	if !res.RequiresFullValidation {
		t.Fatal("expected RequiresFullValidation for unknown path")
	}
	Equal(t, err, nil)
	Equal(t, len(res.ValidatedFields), 0)

	// path navigating into a non struct field cannot be resolved either.
	res, err = validate.StructPartialDependent(Simple{Name: "ok"}, "Name.Length")
	if !res.RequiresFullValidation {
		t.Fatal("expected RequiresFullValidation for unresolvable path")
	}
	Equal(t, err, nil)
}

func TestPartialDependentUnrelatedCustomValidation(t *testing.T) {
	type Custom struct {
		Changed   int `validate:"countme"`
		Dependent int `validate:"countme,gtfield=Changed"`
		Other     int `validate:"countme"`
	}

	var mu sync.Mutex
	counts := map[string]int{}

	validate := New()
	err := validate.RegisterValidation("countme", func(fl FieldLevel) bool {
		mu.Lock()
		counts[fl.StructFieldName()]++
		mu.Unlock()
		return true
	})
	Equal(t, err, nil)

	res, err := validate.StructPartialDependent(Custom{Changed: 1, Dependent: 2, Other: 3}, "Changed")
	if res.RequiresFullValidation {
		t.Fatal("expected partial validation to be possible")
	}
	Equal(t, err, nil)

	// the custom validation on the unrelated Other field must not run.
	Equal(t, counts, map[string]int{"Changed": 1, "Dependent": 1})

	// full validation runs it for every field.
	counts = map[string]int{}
	Equal(t, validate.Struct(Custom{Changed: 1, Dependent: 2, Other: 3}), nil)
	Equal(t, counts, map[string]int{"Changed": 1, "Dependent": 1, "Other": 1})
}

func TestPartialDependentStructLevelFallback(t *testing.T) {
	type WithStructLevel struct {
		Start int
		End   int `validate:"gtfield=Start"`
	}

	validate := New()
	called := 0
	validate.RegisterStructValidation(func(sl StructLevel) {
		called++
	}, WithStructLevel{})

	res, err := validate.StructPartialDependent(WithStructLevel{Start: 10, End: 5}, "Start")
	if !res.RequiresFullValidation {
		t.Fatal("expected RequiresFullValidation when a struct level validation is registered")
	}
	Equal(t, err, nil)
	Equal(t, called, 0)

	// the caller falls back to full validation.
	Equal(t, validate.Struct(WithStructLevel{Start: 1, End: 5}), nil)
	Equal(t, called, 1)
}

func TestPartialDependentCrossLevelFallback(t *testing.T) {
	type Inner struct {
		Start int
	}
	type CrossLevel struct {
		Inner Inner
		End   int `validate:"eqfield=Inner.Start"`
	}
	type WithRequiredIf struct {
		Mode  string
		Value string `validate:"required_if=Mode on"`
	}
	type CrossStruct struct {
		A int
		B int `validate:"gtcsfield=A"`
	}

	validate := New()

	res, err := validate.StructPartialDependent(CrossLevel{Inner: Inner{Start: 1}, End: 1}, "End")
	if !res.RequiresFullValidation {
		t.Fatal("expected RequiresFullValidation for cross level field reference")
	}
	Equal(t, err, nil)

	res, err = validate.StructPartialDependent(WithRequiredIf{Mode: "on"}, "Mode")
	if !res.RequiresFullValidation {
		t.Fatal("expected RequiresFullValidation for unsupported field referencing tag")
	}
	Equal(t, err, nil)

	res, err = validate.StructPartialDependent(CrossStruct{A: 1, B: 2}, "A")
	if !res.RequiresFullValidation {
		t.Fatal("expected RequiresFullValidation for cross struct field reference")
	}
	Equal(t, err, nil)
}

func TestPartialDependentInvalidInput(t *testing.T) {
	validate := New()

	res, err := validate.StructPartialDependent(42, "Field")
	Equal(t, res.RequiresFullValidation, false)
	if _, ok := err.(*InvalidValidationError); !ok {
		t.Fatalf("expected InvalidValidationError, got %T", err)
	}

	res, err = validate.StructPartialDependent(nil, "Field")
	Equal(t, res.RequiresFullValidation, false)
	if _, ok := err.(*InvalidValidationError); !ok {
		t.Fatalf("expected InvalidValidationError, got %T", err)
	}
}

func TestPartialDependentCtx(t *testing.T) {
	type CtxStruct struct {
		Start int
		End   int `validate:"gtfield=Start"`
	}

	validate := New()
	res, err := validate.StructPartialDependentCtx(context.Background(), CtxStruct{Start: 10, End: 5}, "Start")
	if res.RequiresFullValidation {
		t.Fatal("expected partial validation to be possible")
	}
	if err == nil {
		t.Fatal("expected validation errors")
	}
	Equal(t, len(err.(ValidationErrors)), 1)
}

func TestPartialDependentConcurrent(t *testing.T) {
	type DateRange struct {
		Start int `validate:"gte=0"`
		End   int `validate:"gtfield=Start"`
	}

	validate := New()

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(start int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				res, err := validate.StructPartialDependent(DateRange{Start: start, End: 5}, "Start")
				if res.RequiresFullValidation {
					t.Error("expected partial validation to be possible")
					return
				}
				if start >= 5 && err == nil {
					t.Error("expected validation errors")
					return
				}
				if start < 5 && err != nil {
					t.Errorf("expected no errors, got %v", err)
					return
				}
			}
		}(i)
	}
	wg.Wait()
}

func ExampleValidate_StructPartialDependent() {
	type DateRange struct {
		Start int    `validate:"gte=0"`
		End   int    `validate:"gtfield=Start"`
		Name  string `validate:"required"`
	}

	validate := New()

	// only Start was modified by the user, but End depends on it.
	changed := DateRange{Start: 10, End: 5}

	res, err := validate.StructPartialDependent(changed, "Start")
	if res.RequiresFullValidation {
		// dependencies could not be determined, fall back to full validation.
		err = validate.Struct(changed)
	}
	fmtPartial := func() {
		if err == nil {
			fmt.Println("partial: no errors")
			return
		}
		for _, fe := range err.(ValidationErrors) {
			fmt.Printf("partial: %s failed on %s\n", fe.Field(), fe.Tag())
		}
	}
	fmtPartial()

	// full validation also checks the unrelated Name field.
	if err := validate.Struct(changed); err != nil {
		for _, fe := range err.(ValidationErrors) {
			fmt.Printf("full: %s failed on %s\n", fe.Field(), fe.Tag())
		}
	}

	// Output:
	// partial: End failed on gtfield
	// full: End failed on gtfield
	// full: Name failed on required
}
