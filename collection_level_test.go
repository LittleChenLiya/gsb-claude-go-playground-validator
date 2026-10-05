package validator

import (
	"context"
	"reflect"
	"sync"
	"testing"
	"time"
)

// collectionLevelObservation records what a field validator saw through
// the CollectionLevel context.
type collectionLevelObservation struct {
	index     int
	length    int
	kind      reflect.Kind
	parentIdx int
	parentLen int
}

// registerCollectionCheck registers a custom field validator that records
// the collection scope it observes on every call.
func registerCollectionCheck(t *testing.T, validate *Validate, mu *sync.Mutex, observations *[]collectionLevelObservation) {
	t.Helper()

	validate.RegisterValidationCtx("colcheck", func(ctx context.Context, fl FieldLevel) bool {
		cl := fl.CollectionLevel()
		if cl == nil {
			t.Error("expected a non-nil CollectionLevel within a dive, got nil")
			return false
		}

		obs := collectionLevelObservation{
			index:  cl.Index(),
			length: cl.Len(),
			kind:   cl.Collection().Kind(),
		}
		if p := cl.Parent(); p != nil {
			obs.parentIdx = p.Index()
			obs.parentLen = p.Len()
		} else {
			obs.parentIdx = -1
			obs.parentLen = -1
		}

		mu.Lock()
		*observations = append(*observations, obs)
		mu.Unlock()

		return true
	})
}

// TestCollectionLevelNestedDive verifies that rules inside a two-layer
// slice see the correct parent collection and index at each layer, and
// that nested dives have their own scope.
func TestCollectionLevelNestedDive(t *testing.T) {
	validate := New()

	var mu sync.Mutex
	var observations []collectionLevelObservation
	registerCollectionCheck(t, validate, &mu, &observations)

	type Grid struct {
		Rows [][]int `validate:"required,dive,dive,colcheck"`
	}

	grid := Grid{Rows: [][]int{{1, 2}, {3, 4, 5}}}

	if err := validate.Struct(&grid); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Expect one observation per element, in iteration order.
	want := []collectionLevelObservation{
		{index: 0, length: 2, kind: reflect.Array, parentIdx: 0, parentLen: 2},
		{index: 1, length: 2, kind: reflect.Array, parentIdx: 0, parentLen: 2},
		{index: 0, length: 3, kind: reflect.Array, parentIdx: 1, parentLen: 2},
		{index: 1, length: 3, kind: reflect.Array, parentIdx: 1, parentLen: 2},
		{index: 2, length: 3, kind: reflect.Array, parentIdx: 1, parentLen: 2},
	}

	mu.Lock()
	defer mu.Unlock()

	if len(observations) != len(want) {
		t.Fatalf("expected %d observations, got %d: %+v", len(want), len(observations), observations)
	}

	for i, w := range want {
		if observations[i] != w {
			t.Errorf("observation %d: expected %+v, got %+v", i, w, observations[i])
		}
	}
}

// TestCollectionLevelDiveBoundary verifies that the collection scope
// changes when diving into a nested collection and is restored when the
// nested dive completes.
func TestCollectionLevelDiveBoundary(t *testing.T) {
	validate := New()

	var mu sync.Mutex
	var observations []collectionLevelObservation
	registerCollectionCheck(t, validate, &mu, &observations)

	type Line struct {
		Label  string `validate:"colcheck"`
		Values []int  `validate:"required,dive,colcheck"`
		Extra  string `validate:"colcheck"`
	}
	type Order struct {
		Lines []Line `validate:"required,dive"`
	}

	order := Order{Lines: []Line{
		{Label: "a", Values: []int{10, 20}, Extra: "x"},
		{Label: "b", Values: []int{30}, Extra: "y"},
	}}

	if err := validate.Struct(&order); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Expected order of observations:
	//   Line[0].Label       -> outer Lines scope (index 0)
	//   Line[0].Values[0]   -> inner Values scope (index 0, parent Lines 0)
	//   Line[0].Values[1]   -> inner Values scope (index 1, parent Lines 0)
	//   Line[0].Extra       -> outer Lines scope again (index 0)
	//   Line[1].Label       -> outer Lines scope (index 1)
	//   Line[1].Values[0]   -> inner Values scope (index 0, parent Lines 1)
	//   Line[1].Extra       -> outer Lines scope again (index 1)
	want := []collectionLevelObservation{
		{index: 0, length: 2, kind: reflect.Array, parentIdx: -1, parentLen: -1},
		{index: 0, length: 2, kind: reflect.Array, parentIdx: 0, parentLen: 2},
		{index: 1, length: 2, kind: reflect.Array, parentIdx: 0, parentLen: 2},
		{index: 0, length: 2, kind: reflect.Array, parentIdx: -1, parentLen: -1},
		{index: 1, length: 2, kind: reflect.Array, parentIdx: -1, parentLen: -1},
		{index: 0, length: 1, kind: reflect.Array, parentIdx: 1, parentLen: 2},
		{index: 1, length: 2, kind: reflect.Array, parentIdx: -1, parentLen: -1},
	}

	mu.Lock()
	defer mu.Unlock()

	if len(observations) != len(want) {
		t.Fatalf("expected %d observations, got %d: %+v", len(want), len(observations), observations)
	}

	for i, w := range want {
		if observations[i] != w {
			t.Errorf("observation %d: expected %+v, got %+v", i, w, observations[i])
		}
	}
}

type goroutineKey struct{}

// TestCollectionLevelConcurrent verifies that concurrent StructCtx calls
// keep their collection scopes independent and never leak into each
// other.
func TestCollectionLevelConcurrent(t *testing.T) {
	validate := New()

	var mu sync.Mutex
	results := make(map[int][]int)

	validate.RegisterValidationCtx("colcheck", func(ctx context.Context, fl FieldLevel) bool {
		g, _ := ctx.Value(goroutineKey{}).(int)
		cl := fl.CollectionLevel()

		mu.Lock()
		results[g] = append(results[g], cl.Index())
		mu.Unlock()

		return true
	})

	type S struct {
		Items []int `validate:"required,dive,colcheck"`
	}

	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()

			// Vary the slice length per goroutine so a leaked scope would
			// produce wrong indices.
			items := make([]int, 3+g)
			for i := range items {
				items[i] = i
			}

			ctx := context.WithValue(context.Background(), goroutineKey{}, g)
			_ = validate.StructCtx(ctx, &S{Items: items})
		}(g)
	}
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()

	if len(results) != 8 {
		t.Fatalf("expected results for 8 goroutines, got %d: %+v", len(results), results)
	}

	for g := 0; g < 8; g++ {
		want := make([]int, 3+g)
		for i := range want {
			want[i] = i
		}

		got := results[g]
		if len(got) != len(want) {
			t.Errorf("goroutine %d: expected %d indices, got %d: %v", g, len(want), len(got), got)
			continue
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("goroutine %d: expected indices %v, got %v", g, want, got)
				break
			}
		}
	}
}

// TestCollectionLevelCancel verifies that once the context is canceled the
// next element is no longer validated.
func TestCollectionLevelCancel(t *testing.T) {
	validate := New()

	var mu sync.Mutex
	var called []int

	validate.RegisterValidationCtx("colcheck", func(ctx context.Context, fl FieldLevel) bool {
		cl := fl.CollectionLevel()

		mu.Lock()
		called = append(called, cl.Index())
		mu.Unlock()

		// Block until the context is canceled so the test can cancel
		// mid-iteration deterministically.
		<-ctx.Done()

		return true
	})

	type S struct {
		Items []int `validate:"required,dive,colcheck"`
	}

	s := S{Items: []int{0, 1, 2, 3, 4}}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(5 * time.Millisecond)
		cancel()
	}()

	_ = validate.StructCtx(ctx, &s)

	mu.Lock()
	defer mu.Unlock()

	// Only the first element should have been validated: it blocked until
	// cancel, then the loop stopped before the next element.
	if len(called) != 1 || called[0] != 0 {
		t.Errorf("expected only element 0 to be called after cancel, got %v", called)
	}
}

// TestCollectionLevelCancelBeforeStart verifies that a context already
// canceled before validation begins results in no elements being
// validated.
func TestCollectionLevelCancelBeforeStart(t *testing.T) {
	validate := New()

	var mu sync.Mutex
	var called []int

	validate.RegisterValidationCtx("colcheck", func(ctx context.Context, fl FieldLevel) bool {
		cl := fl.CollectionLevel()

		mu.Lock()
		called = append(called, cl.Index())
		mu.Unlock()

		return true
	})

	type S struct {
		Items []int `validate:"required,dive,colcheck"`
	}

	s := S{Items: []int{0, 1, 2, 3, 4}}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_ = validate.StructCtx(ctx, &s)

	mu.Lock()
	defer mu.Unlock()

	if len(called) != 0 {
		t.Errorf("expected no elements to be called with a pre-canceled context, got %v", called)
	}
}

// TestCollectionLevelImmutable verifies that the collection view handed
// to validators is read-only: attempts to modify it panic and the
// original slice is left untouched.
func TestCollectionLevelImmutable(t *testing.T) {
	validate := New()

	type Inner struct {
		X int
	}

	validate.RegisterValidationCtx("colcheck", func(ctx context.Context, fl FieldLevel) bool {
		cl := fl.CollectionLevel()
		if cl == nil {
			return true
		}

		// Replacing an element must panic.
		func() {
			defer func() {
				if recover() == nil {
					t.Error("expected panic when replacing a collection element")
				}
			}()
			cl.Collection().Index(0).Set(reflect.ValueOf(Inner{99}))
		}()

		// Modifying a field of an element must panic.
		func() {
			defer func() {
				if recover() == nil {
					t.Error("expected panic when modifying a collection element field")
				}
			}()
			cl.Collection().Index(0).FieldByName("X").SetInt(99)
		}()

		return true
	})

	type S struct {
		Items []Inner `validate:"required,dive,colcheck"`
	}

	original := []Inner{{1}, {2}, {3}}
	s := S{Items: original}

	_ = validate.Struct(&s)

	if original[0].X != 1 || original[1].X != 2 || original[2].X != 3 {
		t.Errorf("original slice was modified: %+v", original)
	}
}

// TestCollectionLevelMap verifies that diving into a map exposes a
// read-only collection view with no meaningful index.
func TestCollectionLevelMap(t *testing.T) {
	validate := New()

	var mu sync.Mutex
	var observations []collectionLevelObservation
	registerCollectionCheck(t, validate, &mu, &observations)

	type S struct {
		Items map[string]int `validate:"required,dive,colcheck"`
	}

	s := S{Items: map[string]int{"a": 1, "b": 2}}

	if err := validate.Struct(&s); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()

	if len(observations) != 2 {
		t.Fatalf("expected 2 observations, got %d: %+v", len(observations), observations)
	}

	for _, obs := range observations {
		if obs.index != -1 {
			t.Errorf("expected map index to be -1, got %d", obs.index)
		}
		if obs.kind != reflect.Map {
			t.Errorf("expected map kind, got %v", obs.kind)
		}
		if obs.parentIdx != -1 {
			t.Errorf("expected no parent for outermost map dive, got %d", obs.parentIdx)
		}
	}
}

// TestCollectionLevelNotDiving verifies that CollectionLevel returns nil
// when the field is not being validated within a dive.
func TestCollectionLevelNotDiving(t *testing.T) {
	validate := New()

	var called bool
	validate.RegisterValidationCtx("colcheck", func(ctx context.Context, fl FieldLevel) bool {
		called = true
		if cl := fl.CollectionLevel(); cl != nil {
			t.Errorf("expected nil CollectionLevel outside a dive, got %+v", cl)
		}
		return true
	})

	type S struct {
		Name string `validate:"colcheck"`
	}

	_ = validate.Struct(&S{Name: "ok"})

	if !called {
		t.Error("expected validator to be called")
	}
}

// TestCollectionLevelVar verifies that CollectionLevel works when
// validating a top-level slice via VarCtx.
func TestCollectionLevelVar(t *testing.T) {
	validate := New()

	var mu sync.Mutex
	var observations []collectionLevelObservation
	registerCollectionCheck(t, validate, &mu, &observations)

	items := []int{10, 20, 30}
	_ = validate.VarCtx(context.Background(), items, "required,dive,colcheck")

	mu.Lock()
	defer mu.Unlock()

	if len(observations) != 3 {
		t.Fatalf("expected 3 observations, got %d: %+v", len(observations), observations)
	}

	for i, obs := range observations {
		if obs.index != i || obs.length != 3 {
			t.Errorf("observation %d: expected index %d length 3, got index %d length %d", i, i, obs.index, obs.length)
		}
		if obs.parentIdx != -1 {
			t.Errorf("observation %d: expected no parent, got %d", i, obs.parentIdx)
		}
	}
}

// TestCollectionLevelArray verifies that diving into an array exposes a
// read-only collection view that rejects modification.
func TestCollectionLevelArray(t *testing.T) {
	validate := New()

	validate.RegisterValidationCtx("colcheck", func(ctx context.Context, fl FieldLevel) bool {
		cl := fl.CollectionLevel()
		if cl == nil {
			return true
		}

		// Modifying an array element must panic.
		func() {
			defer func() {
				if recover() == nil {
					t.Error("expected panic when modifying an array element")
				}
			}()
			cl.Collection().Index(0).SetInt(99)
		}()

		return true
	})

	type S struct {
		Items [3]int `validate:"required,dive,colcheck"`
	}

	original := [3]int{1, 2, 3}
	s := S{Items: original}

	_ = validate.Struct(&s)

	if original[0] != 1 || original[1] != 2 || original[2] != 3 {
		t.Errorf("original array was modified: %+v", original)
	}
}
