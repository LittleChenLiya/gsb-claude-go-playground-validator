package validator

import (
	"context"
	"fmt"
)

// ExampleValidate_StructWithBudget shows a ~90 second terminal walkthrough of
// budgeted validation: the collected error paths, the number of visited
// nodes and the independent stop reason. A truncated result is reported as
// truncated, never as a completed full validation.
func ExampleValidate_StructWithBudget() {
	type Line struct {
		SKU string `validate:"required"`
	}
	type Cart struct {
		Lines []Line `validate:"dive"`
	}

	// three lines, every SKU empty -> three "required" failures without a budget
	cart := Cart{Lines: make([]Line, 3)}

	errs, res, err := New().StructWithBudget(context.Background(), cart, Budget{MaxNodes: 100, MaxErrors: 2})
	if err != nil {
		fmt.Println("error:", err)
		return
	}

	for _, e := range errs {
		fmt.Printf("path=%s tag=%s\n", e.Namespace(), e.Tag())
	}
	fmt.Println("visited:", res.Nodes)
	fmt.Println("stop:", res.Reason)
	if res.Truncated {
		fmt.Println("result: truncated, more errors may exist")
	} else {
		fmt.Println("result: complete")
	}

	// Output:
	// path=Cart.Lines[0].SKU tag=required
	// path=Cart.Lines[1].SKU tag=required
	// visited: 5
	// stop: MaxErrors
	// result: truncated, more errors may exist
}
