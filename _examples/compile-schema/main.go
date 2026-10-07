package main

import (
	"fmt"
	"reflect"

	"github.com/go-playground/validator/v10"
)

// Server is a nested struct with (intentionally) broken validation tags:
// a bad parameter and a dangling cross-field reference.
type Server struct {
	Port     int    `validate:"min=abc"`              // bad numeric parameter
	Password string `validate:"eqfield=DoesNotExist"` // cross-field reference to a field that does not exist
}

type Config struct {
	Name   string `validate:"required"`
	Server Server
}

// User is a valid schema used to show plan reuse.
type User struct {
	Name    string   `validate:"required"`
	Email   string   `validate:"required,email"`
	Friends []string `validate:"dive,required"`
}

func main() {
	validate := validator.New()

	// 1. Pre-compilation rejects the broken schema, reporting ALL problems
	//    at once with their field paths, without calling any validation
	//    function or reading any value.
	_, err := validate.CompileSchema(reflect.TypeOf(Config{}))
	if err != nil {
		fmt.Println("CompileSchema rejected Config:")
		for _, se := range err.(validator.SchemaErrors) {
			fmt.Printf("  path=%s tag=%s param=%s kind=%d\n    %s\n", se.Path, se.Tag, se.Param, se.Kind, se)
		}
	}

	// 2. A valid schema compiles into an immutable, reusable plan.
	plan, err := validate.CompileSchema(reflect.TypeOf(User{}))
	if err != nil {
		panic(err)
	}
	fmt.Println("\nplan compiled for:", plan.Type())

	// 3. The plan can be reused any number of times; results match Struct.
	for i, u := range []User{
		{Name: "gopher", Email: "gopher@example.com", Friends: []string{"rob"}},
		{Name: "", Email: "not-an-email", Friends: []string{""}},
	} {
		err := validate.StructWithPlan(u, plan)
		fmt.Printf("StructWithPlan #%d: %v\n", i+1, err)
	}
}
