package main

import (
	"fmt"
	"reflect"

	"github.com/go-playground/validator/v10"
)

// This example shows how to opt into the collection validation context
// with WithCollectionContext and register a custom rule that inspects
// sibling elements and the current index of the collection being dived
// into - without passing state through global variables.
func main() {
	validate := validator.New(validator.WithCollectionContext())

	// unique_within_siblings fails when the current element appears more
	// than once in the enclosing slice.
	err := validate.RegisterValidation("unique_within_siblings", func(fl validator.FieldLevel) bool {
		view, ok := fl.Collection()
		if !ok {
			return true // not validating inside a collection dive
		}
		idx, _ := fl.CollectionIndex()
		for i := 0; i < view.Len(); i++ {
			if i != idx && reflect.DeepEqual(view.Index(i).Interface(), fl.Field().Interface()) {
				return false
			}
		}
		return true
	})
	if err != nil {
		panic(err)
	}

	// ascending fails when the current element is smaller than its
	// predecessor within the same slice.
	err = validate.RegisterValidation("ascending", func(fl validator.FieldLevel) bool {
		view, ok := fl.Collection()
		if !ok {
			return true
		}
		idx, _ := fl.CollectionIndex()
		if idx == 0 {
			return true
		}
		prev := view.Index(idx - 1)
		cur := fl.Field()
		if prev.Kind() == reflect.Int && cur.Kind() == reflect.Int {
			return cur.Int() >= prev.Int()
		}
		return true
	})
	if err != nil {
		panic(err)
	}

	type Survey struct {
		Tags   []string `validate:"dive,unique_within_siblings"`
		Scores [][]int  `validate:"dive,dive,ascending"`
	}

	good := Survey{
		Tags:   []string{"go", "validator"},
		Scores: [][]int{{1, 2, 3}, {4, 5}},
	}
	fmt.Println("good valid:", validate.Struct(good) == nil)

	bad := Survey{
		Tags:   []string{"go", "validator", "go"},
		Scores: [][]int{{1, 3, 2}},
	}
	if err := validate.Struct(bad); err != nil {
		for _, e := range err.(validator.ValidationErrors) {
			fmt.Println("invalid:", e.Namespace(), "failed on", e.Tag())
		}
	}
}
