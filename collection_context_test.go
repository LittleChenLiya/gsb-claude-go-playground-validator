package validator

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
)

// collectionObservation records what a custom rule observed through the
// collection context for a single field validation.
type collectionObservation struct {
	field     interface{}
	idx       int
	idxOK     bool
	viewOK    bool
	viewLen   int
	viewKind  reflect.Kind
	selfMatch bool // view.Index(idx) deep-equals fl.Field()
}

func observeCollection(fl FieldLevel) collectionObservation {
	obs := collectionObservation{field: fl.Field().Interface()}
	view, ok := fl.Collection()
	obs.viewOK = ok
	obs.idx, obs.idxOK = fl.CollectionIndex()
	if ok {
		obs.viewLen = view.Len()
		obs.viewKind = view.Kind()
		if obs.idxOK && obs.idx >= 0 && obs.idx < view.Len() {
			obs.selfMatch = reflect.DeepEqual(view.Index(obs.idx).Interface(), fl.Field().Interface())
		}
	}
	return obs
}

func TestCollectionContextNestedDiveScopes(t *testing.T) {
	v := New(WithCollectionContext())

	var outerSeen, innerSeen []collectionObservation

	if err := v.RegisterValidation("outerrec", func(fl FieldLevel) bool {
		outerSeen = append(outerSeen, observeCollection(fl))
		return true
	}); err != nil {
		t.Fatalf("failed to register outerrec: %v", err)
	}
	if err := v.RegisterValidation("innerrec", func(fl FieldLevel) bool {
		innerSeen = append(innerSeen, observeCollection(fl))
		return true
	}); err != nil {
		t.Fatalf("failed to register innerrec: %v", err)
	}

	type Matrix struct {
		Groups [][]int `validate:"dive,outerrec,dive,innerrec"`
	}

	m := Matrix{Groups: [][]int{{10, 11}, {20}}}
	if err := v.Struct(m); err != nil {
		t.Fatalf("unexpected validation error: %v", err)
	}

	// outer rule: sees the [][]int parent collection with the outer index
	if len(outerSeen) != 2 {
		t.Fatalf("expected 2 outer observations, got %d", len(outerSeen))
	}
	for i, obs := range outerSeen {
		if !obs.viewOK || !obs.idxOK {
			t.Fatalf("outer[%d]: expected collection context, got viewOK=%v idxOK=%v", i, obs.viewOK, obs.idxOK)
		}
		if obs.idx != i {
			t.Errorf("outer[%d]: expected index %d, got %d", i, i, obs.idx)
		}
		if obs.viewLen != 2 || obs.viewKind != reflect.Slice {
			t.Errorf("outer[%d]: expected parent collection of kind Slice len 2, got %v len %d", i, obs.viewKind, obs.viewLen)
		}
		if !obs.selfMatch {
			t.Errorf("outer[%d]: view.Index(%d) did not match the field under validation", i, obs.idx)
		}
	}

	// inner rule: sees each inner []int parent collection with the inner index
	expectedInner := []struct {
		field int
		idx   int
		len   int
	}{
		{10, 0, 2},
		{11, 1, 2},
		{20, 0, 1},
	}
	if len(innerSeen) != len(expectedInner) {
		t.Fatalf("expected %d inner observations, got %d", len(expectedInner), len(innerSeen))
	}
	for i, want := range expectedInner {
		obs := innerSeen[i]
		if !obs.viewOK || !obs.idxOK {
			t.Fatalf("inner[%d]: expected collection context, got viewOK=%v idxOK=%v", i, obs.viewOK, obs.idxOK)
		}
		if obs.field != want.field || obs.idx != want.idx || obs.viewLen != want.len {
			t.Errorf("inner[%d]: expected field=%d idx=%d len=%d, got field=%v idx=%d len=%d",
				i, want.field, want.idx, want.len, obs.field, obs.idx, obs.viewLen)
		}
		if !obs.selfMatch {
			t.Errorf("inner[%d]: view.Index(%d) did not match the field under validation", i, obs.idx)
		}
	}
}

func TestCollectionContextDiveBoundaries(t *testing.T) {
	v := New(WithCollectionContext())

	type boundaryObs struct {
		field  string
		viewOK bool
		idxOK  bool
	}
	var outside []boundaryObs
	var firstLens, secondLens []int

	mustRegister := func(tag string, fn Func) {
		if err := v.RegisterValidation(tag, fn); err != nil {
			t.Fatalf("failed to register %s: %v", tag, err)
		}
	}
	mustRegister("outsiderec", func(fl FieldLevel) bool {
		_, viewOK := fl.Collection()
		_, idxOK := fl.CollectionIndex()
		outside = append(outside, boundaryObs{field: fl.Field().String(), viewOK: viewOK, idxOK: idxOK})
		return true
	})
	mustRegister("firstrec", func(fl FieldLevel) bool {
		view, ok := fl.Collection()
		if !ok {
			t.Error("firstrec: expected collection context inside dive")
			return true
		}
		firstLens = append(firstLens, view.Len())
		return true
	})
	mustRegister("secondrec", func(fl FieldLevel) bool {
		view, ok := fl.Collection()
		if !ok {
			t.Error("secondrec: expected collection context inside dive")
			return true
		}
		secondLens = append(secondLens, view.Len())
		return true
	})

	type Boundary struct {
		Before string `validate:"outsiderec"`
		First  []int  `validate:"dive,firstrec"`
		Middle string `validate:"outsiderec"`
		Second []int  `validate:"dive,secondrec"`
		After  string `validate:"outsiderec"`
	}

	b := Boundary{
		Before: "b",
		First:  []int{1, 2},
		Middle: "m",
		Second: []int{3, 4, 5},
		After:  "a",
	}
	if err := v.Struct(b); err != nil {
		t.Fatalf("unexpected validation error: %v", err)
	}

	// rules outside any dive must not observe a collection scope, even
	// after a dive on a previous field completed (scope popped)
	if len(outside) != 3 {
		t.Fatalf("expected 3 outside observations, got %d", len(outside))
	}
	for i, obs := range outside {
		if obs.viewOK || obs.idxOK {
			t.Errorf("outside[%d] (%q): collection scope leaked outside dive boundary", i, obs.field)
		}
	}

	// sibling slice fields must each see their own collection
	if len(firstLens) != 2 {
		t.Fatalf("expected 2 firstrec observations, got %d", len(firstLens))
	}
	for i, l := range firstLens {
		if l != 2 {
			t.Errorf("firstrec[%d]: expected collection len 2, got %d", i, l)
		}
	}
	if len(secondLens) != 3 {
		t.Fatalf("expected 3 secondrec observations, got %d", len(secondLens))
	}
	for i, l := range secondLens {
		if l != 3 {
			t.Errorf("secondrec[%d]: expected collection len 3, got %d", i, l)
		}
	}
}

func TestCollectionContextMapDive(t *testing.T) {
	v := New(WithCollectionContext())

	calls := 0
	if err := v.RegisterValidation("maprec", func(fl FieldLevel) bool {
		calls++
		view, ok := fl.Collection()
		if !ok {
			t.Error("expected collection context inside map dive")
			return true
		}
		if view.Kind() != reflect.Map || view.Len() != 2 {
			t.Errorf("expected map view of len 2, got %v len %d", view.Kind(), view.Len())
		}
		if _, ok := fl.CollectionIndex(); ok {
			t.Error("map dives must not report a positional index")
		}
		if got := view.MapIndex(reflect.ValueOf("a")); !got.IsValid() || got.Int() != 1 {
			t.Errorf("expected read-only MapIndex(a)=1, got %v", got)
		}
		if got := view.Index(0); got.IsValid() {
			t.Errorf("Index on a map view must return the zero Value, got %v", got)
		}
		return true
	}); err != nil {
		t.Fatalf("failed to register maprec: %v", err)
	}

	type Holder struct {
		M map[string]int `validate:"dive,maprec"`
	}
	if err := v.Struct(Holder{M: map[string]int{"a": 1, "b": 2}}); err != nil {
		t.Fatalf("unexpected validation error: %v", err)
	}
	if calls != 2 {
		t.Fatalf("expected 2 maprec calls, got %d", calls)
	}
}

func TestCollectionContextCancellationStopsIteration(t *testing.T) {
	v := New(WithCollectionContext())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	calls := 0
	if err := v.RegisterValidation("cancelrec", func(fl FieldLevel) bool {
		calls++
		cancel() // cancel during the first item's validation
		return true
	}); err != nil {
		t.Fatalf("failed to register cancelrec: %v", err)
	}

	type Holder struct {
		Nums []int `validate:"dive,cancelrec"`
	}
	if err := v.StructCtx(ctx, Holder{Nums: []int{1, 2, 3, 4, 5}}); err != nil {
		t.Fatalf("unexpected validation error: %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected rule to be called only once before cancellation, got %d calls", calls)
	}
}

func TestCollectionContextConcurrentIsolation(t *testing.T) {
	v := New(WithCollectionContext())

	type ctxKey struct{}
	if err := v.RegisterValidationCtx("isolationrec", func(ctx context.Context, fl FieldLevel) bool {
		wantLen, _ := ctx.Value(ctxKey{}).(int)
		view, ok := fl.Collection()
		if !ok {
			t.Error("expected collection context inside dive")
			return true
		}
		idx, ok := fl.CollectionIndex()
		if !ok {
			t.Error("expected collection index inside dive")
			return true
		}
		if view.Len() != wantLen {
			t.Errorf("collection context crossed requests: expected len %d, got %d", wantLen, view.Len())
			return true
		}
		if !reflect.DeepEqual(view.Index(idx).Interface(), fl.Field().Interface()) {
			t.Errorf("collection context crossed requests: view.Index(%d) != current field", idx)
		}
		return true
	}); err != nil {
		t.Fatalf("failed to register isolationrec: %v", err)
	}

	type Holder struct {
		Nums []int `validate:"dive,isolationrec"`
	}

	var wg sync.WaitGroup
	for _, nums := range [][]int{
		{1, 2},
		{3, 4, 5, 6, 7},
		{8},
		{9, 10, 11},
	} {
		nums := nums
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				ctx := context.WithValue(context.Background(), ctxKey{}, len(nums))
				if err := v.StructCtx(ctx, Holder{Nums: nums}); err != nil {
					t.Errorf("unexpected validation error: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()
}

func TestCollectionViewReadOnly(t *testing.T) {
	v := New(WithCollectionContext())

	var view CollectionView
	if err := v.RegisterValidation("capturerec", func(fl FieldLevel) bool {
		view, _ = fl.Collection()
		return true
	}); err != nil {
		t.Fatalf("failed to register capturerec: %v", err)
	}

	type Holder struct {
		Nums []int `validate:"dive,capturerec"`
	}
	h := Holder{Nums: []int{1, 2, 3}}
	if err := v.Struct(h); err != nil {
		t.Fatalf("unexpected validation error: %v", err)
	}

	if !view.IsValid() {
		t.Fatal("expected a valid captured view")
	}

	// mutating through the view's API is explicitly rejected
	if err := view.Set(0, 99); !errors.Is(err, ErrCollectionViewReadOnly) {
		t.Fatalf("expected ErrCollectionViewReadOnly, got %v", err)
	}

	// elements handed out by the view are not settable
	elem := view.Index(0)
	if !elem.IsValid() {
		t.Fatal("expected valid element from view")
	}
	if elem.CanSet() {
		t.Fatal("view element must not be settable")
	}
	if elem.Int() != 1 {
		t.Fatalf("expected element value 1, got %d", elem.Int())
	}

	// the original slice is untouched
	if !reflect.DeepEqual(h.Nums, []int{1, 2, 3}) {
		t.Fatalf("original slice was modified: %v", h.Nums)
	}

	// zero view is safe to use
	var zero CollectionView
	if zero.IsValid() || zero.Len() != 0 || zero.Kind() != reflect.Invalid {
		t.Fatal("zero CollectionView must report no collection")
	}
	if got := zero.Index(0); got.IsValid() {
		t.Fatal("zero CollectionView must return invalid elements")
	}
	if err := zero.Set(0, 1); !errors.Is(err, ErrCollectionViewReadOnly) {
		t.Fatal("zero CollectionView must reject mutation")
	}
}

func TestCollectionContextDisabledByDefault(t *testing.T) {
	v := New()

	called := false
	if err := v.RegisterValidation("defaultrec", func(fl FieldLevel) bool {
		called = true
		if _, ok := fl.Collection(); ok {
			t.Error("collection context must not be available without WithCollectionContext")
		}
		if _, ok := fl.CollectionIndex(); ok {
			t.Error("collection index must not be available without WithCollectionContext")
		}
		return true
	}); err != nil {
		t.Fatalf("failed to register defaultrec: %v", err)
	}

	type Holder struct {
		Nums []int `validate:"dive,defaultrec"`
	}
	if err := v.Struct(Holder{Nums: []int{1, 2}}); err != nil {
		t.Fatalf("unexpected validation error: %v", err)
	}
	if !called {
		t.Fatal("expected rule to be called")
	}
}

func ExampleWithCollectionContext() {
	validate := New(WithCollectionContext())

	// unique_within_siblings fails when the current element appears more
	// than once in the enclosing slice. The rule reads its siblings and
	// its own index straight from the FieldLevel collection context, no
	// global state required.
	_ = validate.RegisterValidation("unique_within_siblings", func(fl FieldLevel) bool {
		view, ok := fl.Collection()
		if !ok {
			return true
		}
		idx, _ := fl.CollectionIndex()
		for i := 0; i < view.Len(); i++ {
			if i != idx && reflect.DeepEqual(view.Index(i).Interface(), fl.Field().Interface()) {
				return false
			}
		}
		return true
	})

	type Team struct {
		Tags []string `validate:"dive,unique_within_siblings"`
	}

	fmt.Println(validate.Struct(Team{Tags: []string{"go", "validator"}}) == nil)
	fmt.Println(validate.Struct(Team{Tags: []string{"go", "validator", "go"}}) == nil)
	// Output:
	// true
	// false
}
