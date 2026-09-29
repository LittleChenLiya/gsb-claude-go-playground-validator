package validator

import (
	"fmt"
	"testing"
	"time"

	. "github.com/go-playground/assert/v2"
)

type changedPeriod struct {
	Start time.Time
	End   time.Time `validate:"gtfield=Start"`
}

// TestStructChangedStartEnd is the core acceptance test: Start and End have an
// ordering relation, changing only Start must still surface that End is no longer
// valid.
func TestStructChangedStartEnd(t *testing.T) {
	validate := New()

	now := time.Now()
	p := &changedPeriod{Start: now.Add(2 * time.Hour), End: now.Add(time.Hour)}

	// sanity: full validation reports End
	fullErr := validate.Struct(p)
	NotEqual(t, fullErr, nil)
	AssertError(t, fullErr, "changedPeriod.End", "changedPeriod.End", "End", "End", "gtfield")

	// only Start changed, but the dependency on End must not be missed
	err, requiresFull := validate.StructChanged(p, "Start")
	Equal(t, requiresFull, false)
	NotEqual(t, err, nil)

	errs, ok := err.(ValidationErrors)
	Equal(t, ok, true)
	Equal(t, len(errs), len(fullErr.(ValidationErrors)))
	AssertError(t, errs, "changedPeriod.End", "changedPeriod.End", "End", "End", "gtfield")

	// valid state: neither full nor partial validation report anything
	p.Start = now
	p.End = now.Add(time.Hour)

	err, requiresFull = validate.StructChanged(p, "Start")
	Equal(t, requiresFull, false)
	Equal(t, err, nil)
}

type changedChain struct {
	A int `validate:"countme"`
	B int `validate:"countme,gtefield=A"`
	C int `validate:"countme,gtfield=B"`
	D int `validate:"countme"`
}

func TestStructChangedChainedDependencies(t *testing.T) {
	counts := make(map[string]int)
	validate := New()
	err := validate.RegisterValidation("countme", func(fl FieldLevel) bool {
		counts[fl.StructFieldName()]++
		return true
	})
	Equal(t, err, nil)

	s := &changedChain{A: 5, B: 1, C: 1, D: 0}

	// changing A transitively affects B (gtefield=A) and C (gtfield=B), but not D
	verr, requiresFull := validate.StructChanged(s, "A")
	Equal(t, requiresFull, false)
	NotEqual(t, verr, nil)

	errs := verr.(ValidationErrors)
	Equal(t, len(errs), 2)
	AssertError(t, errs, "changedChain.B", "changedChain.B", "B", "B", "gtefield")

	// every affected field validated exactly once, unrelated field never validated
	Equal(t, counts["A"], 1)
	Equal(t, counts["B"], 1)
	Equal(t, counts["C"], 1)
	Equal(t, counts["D"], 0)
}

type changedCycle struct {
	X string `validate:"countme,nefield=Y"`
	Y string `validate:"countme,nefield=X"`
	Z string `validate:"countme"`
}

func TestStructChangedDependencyCycle(t *testing.T) {
	counts := make(map[string]int)
	validate := New()
	err := validate.RegisterValidation("countme", func(fl FieldLevel) bool {
		counts[fl.StructFieldName()]++
		return true
	})
	Equal(t, err, nil)

	s := &changedCycle{X: "a", Y: "a", Z: "z"}

	// X <-> Y dependency cycle must terminate and validate each field at most once
	verr, requiresFull := validate.StructChanged(s, "X")
	Equal(t, requiresFull, false)
	NotEqual(t, verr, nil)

	errs := verr.(ValidationErrors)
	Equal(t, len(errs), 2)

	Equal(t, counts["X"], 1)
	Equal(t, counts["Y"], 1)
	Equal(t, counts["Z"], 0)
}

type changedInner struct {
	Start int
	End   int `validate:"gtfield=Start"`
}

type changedOuter struct {
	Inner changedInner
	Ptr   *changedInner
	Name  string `validate:"required"`
}

func TestStructChangedNestedStruct(t *testing.T) {
	validate := New()

	s := &changedOuter{
		Inner: changedInner{Start: 5, End: 3},
		Ptr:   &changedInner{Start: 5, End: 3},
		Name:  "ok",
	}

	// nested value struct
	err, requiresFull := validate.StructChanged(s, "Inner.Start")
	Equal(t, requiresFull, false)
	NotEqual(t, err, nil)
	AssertError(t, err, "changedOuter.Inner.End", "changedOuter.Inner.End", "End", "End", "gtfield")

	// nested pointer struct
	err, requiresFull = validate.StructChanged(s, "Ptr.Start")
	Equal(t, requiresFull, false)
	NotEqual(t, err, nil)
	AssertError(t, err, "changedOuter.Ptr.End", "changedOuter.Ptr.End", "End", "End", "gtfield")

	// changing the whole nested struct revalidates its subtree
	err, requiresFull = validate.StructChanged(s, "Inner")
	Equal(t, requiresFull, false)
	NotEqual(t, err, nil)
	AssertError(t, err, "changedOuter.Inner.End", "changedOuter.Inner.End", "End", "End", "gtfield")

	// unknown nested path falls back to full validation
	err, requiresFull = validate.StructChanged(s, "Inner.Nope")
	Equal(t, requiresFull, true)
	Equal(t, err, nil)
}

func TestStructChangedUnknownPath(t *testing.T) {
	validate := New()

	s := &changedPeriod{Start: time.Now(), End: time.Now()}

	err, requiresFull := validate.StructChanged(s, "Nope")
	Equal(t, requiresFull, true)
	Equal(t, err, nil)

	err, requiresFull = validate.StructChanged(s, "")
	Equal(t, requiresFull, true)
	Equal(t, err, nil)
}

func TestStructChangedStructLevelFallback(t *testing.T) {
	type slStruct struct {
		Start int
		End   int `validate:"gtfield=Start"`
	}

	validate := New()
	called := 0
	validate.RegisterStructValidation(func(sl StructLevel) {
		called++
	}, slStruct{})

	// custom struct-level validation cannot be dependency-analyzed
	err, requiresFull := validate.StructChanged(&slStruct{Start: 5, End: 3}, "Start")
	Equal(t, requiresFull, true)
	Equal(t, err, nil)
	Equal(t, called, 0)
}

func TestStructChangedCrossLevelFallback(t *testing.T) {
	// cross-struct field references cannot be tracked
	type crossStruct struct {
		A string
		B string `validate:"eqcsfield=A"`
	}

	validate := New()
	err, requiresFull := validate.StructChanged(&crossStruct{A: "a", B: "a"}, "A")
	Equal(t, requiresFull, true)
	Equal(t, err, nil)

	// conditional rules referencing other fields cannot be tracked either
	type condStruct struct {
		A string
		B string `validate:"required_with=A"`
	}

	err, requiresFull = validate.StructChanged(&condStruct{A: "a", B: "b"}, "A")
	Equal(t, requiresFull, true)
	Equal(t, err, nil)

	// untrackable references hiding in nested structs must not be missed either
	type nestedCross struct {
		Inner crossStruct
		A     string
	}

	err, requiresFull = validate.StructChanged(&nestedCross{}, "A")
	Equal(t, requiresFull, true)
	Equal(t, err, nil)
}

func TestStructChangedUnrelatedCustomValidationNotCalled(t *testing.T) {
	counts := make(map[string]int)
	validate := New()
	err := validate.RegisterValidation("countme", func(fl FieldLevel) bool {
		counts[fl.StructFieldName()]++
		return true
	})
	Equal(t, err, nil)

	type customStruct struct {
		Changed   string `validate:"countme"`
		Unrelated string `validate:"countme"`
		Other     int
	}

	verr, requiresFull := validate.StructChanged(&customStruct{Changed: "a", Unrelated: "b"}, "Changed")
	Equal(t, requiresFull, false)
	Equal(t, verr, nil)

	// the custom validation of the changed field ran, the unrelated one did not
	Equal(t, counts["Changed"], 1)
	Equal(t, counts["Unrelated"], 0)
}

func TestStructChangedInvalidInput(t *testing.T) {
	validate := New()

	err, requiresFull := validate.StructChanged(42, "X")
	Equal(t, requiresFull, false)
	NotEqual(t, err, nil)
	_, ok := err.(*InvalidValidationError)
	Equal(t, ok, true)

	// no changed fields: nothing to validate, no fallback needed
	err, requiresFull = validate.StructChanged(&changedPeriod{})
	Equal(t, requiresFull, false)
	Equal(t, err, nil)
}

// ExampleValidate_StructChanged compares a full validation with a dependency-aware
// partial validation after only the Start field of a form was modified.
func ExampleValidate_StructChanged() {
	validate := New()

	type Event struct {
		Name  string `validate:"required"`
		Start int
		End   int `validate:"gtfield=Start"`
	}

	fieldNames := func(err error) []string {
		if err == nil {
			return nil
		}
		names := make([]string, 0, len(err.(ValidationErrors)))
		for _, e := range err.(ValidationErrors) {
			names = append(names, e.Field())
		}
		return names
	}

	// the form only updated Start, leaving End stale
	event := &Event{Name: "launch", Start: 10, End: 5}

	// full validation, for comparison
	fmt.Println("full:", fieldNames(validate.Struct(event)))

	// dependency-aware partial validation of the changed field only:
	// End is validated as well because its gtfield rule depends on Start,
	// while the unrelated Name field is skipped.
	partialErr, requiresFull := validate.StructChanged(event, "Start")
	fmt.Println("partial:", fieldNames(partialErr), "requiresFull:", requiresFull)

	// Output:
	// full: [End]
	// partial: [End] requiresFull: false
}
