// Structured errors example: converts validator.ValidationErrors into a
// machine-readable form API clients can use to locate failing request fields.
//
// Run from the repository root:
//
//	go run ./_examples/structured-errors
package main

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/go-playground/validator/v10"
)

type Item struct {
	Name string `json:"name" validate:"required"`
}

type CreateRequest struct {
	Items    []Item            `json:"items" validate:"required,dive"`
	Labels   map[string]string `json:"labels" validate:"dive,required"`
	Counts   map[int]string    `json:"counts" validate:"dive,required"`
	Password string            `json:"password" validate:"required,min=8"`
}

// structLevelDemo reports one provable and one unprovable custom path.
type structLevelDemo struct {
	Name string `json:"name"`
}

func main() {
	validate := validator.New()
	validate.RegisterTagNameFunc(func(fld reflect.StructField) string {
		name := strings.SplitN(fld.Tag.Get("json"), ",", 2)[0]
		if name == "-" {
			return ""
		}
		return name
	})

	req := CreateRequest{
		Items:  []Item{{Name: "ok"}, {Name: ""}},
		Labels: map[string]string{"a/b~c": ""},
		Counts: map[int]string{5: ""},
	}

	if err := validate.Struct(req); err != nil {
		errs := err.(validator.ValidationErrors)

		// values are excluded by default so secrets such as passwords are
		// not leaked; pass validator.WithValues() to opt in.
		out, _ := json.MarshalIndent(errs.Structured(), "", "  ")
		fmt.Println("== structured errors ==")
		fmt.Println(string(out))
	}

	validate.RegisterStructValidation(func(sl validator.StructLevel) {
		d := sl.Current().Interface().(structLevelDemo)
		// provable: "Name" is a real field of the struct
		sl.ReportError(d.Name, "Name", "Name", "namepolicy", "")
		// unprovable custom path: marked unresolved, namespace preserved
		sl.ReportError(d.Name, "Custom[0].Path", "Custom[0].Path", "custom", "")
	}, structLevelDemo{})

	if err := validate.Struct(structLevelDemo{Name: "bad"}); err != nil {
		errs := err.(validator.ValidationErrors)
		out, _ := json.MarshalIndent(errs.Structured(), "", "  ")
		fmt.Println("== struct level (resolved + unresolved) ==")
		fmt.Println(string(out))
	}
}
