package main

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/go-playground/validator/v10"
)

// Item is a nested struct validated through a slice dive.
type Item struct {
	Name string `json:"name" validate:"required"`
}

// Order exercises slice indexes, string map keys, non-string map keys and a
// password field whose value must not leak into the API error response.
type Order struct {
	Items    []Item            `json:"items" validate:"dive"`
	Labels   map[string]string `json:"labels" validate:"dive,keys,required,endkeys,required"`
	Meta     map[int]string    `json:"meta" validate:"dive,required"`
	Password string            `json:"password" validate:"min=8"`
}

// Profile has a struct-level validation reporting an unprovable custom path.
type Profile struct {
	Name string `json:"name"`
}

func main() {
	validate := validator.New()

	// external names follow the json tags, falling back to the Go field name
	validate.RegisterTagNameFunc(func(fld reflect.StructField) string {
		name := strings.SplitN(fld.Tag.Get("json"), ",", 2)[0]
		if name == "-" {
			return ""
		}
		return name
	})

	validate.RegisterStructValidation(func(sl validator.StructLevel) {
		p := sl.Current().Interface().(Profile)
		if p.Name == "" {
			// custom path that cannot be proven to map to a real field,
			// it is reported as unresolved with the original namespace kept.
			sl.ReportError(nil, "audit[0].trail", "Audit[0].Trail", "required", "")
		}
	}, Profile{})

	order := Order{
		Items:    []Item{{Name: "ok"}, {}},
		Labels:   map[string]string{"a/b~c": ""},
		Meta:     map[int]string{7: ""},
		Password: "secret",
	}

	fmt.Println("== structured errors (values omitted by default) ==")
	printStructured(validate.Struct(order))

	fmt.Println("== struct-level custom path, unresolved ==")
	printStructured(validate.Struct(Profile{}))
}

func printStructured(err error) {
	if err == nil {
		fmt.Println("no errors")
		return
	}
	ve, ok := err.(validator.ValidationErrors)
	if !ok {
		fmt.Println("invalid validation error:", err)
		return
	}
	out, _ := json.MarshalIndent(ve.Structured(), "", "  ")
	fmt.Println(string(out))
}
