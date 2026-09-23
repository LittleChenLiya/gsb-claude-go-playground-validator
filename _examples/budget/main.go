package main

import (
	"context"
	"fmt"

	"github.com/go-playground/validator/v10"
)

// StructWithBudget in 90 seconds.
//
// Run from the repository root:
//
//	go run ./_examples/budget/main.go
//
// Every scenario prints the failing field paths, how many nodes were
// visited and why traversal stopped. Watch the second scenario: a
// truncated result is a PARTIAL prefix of the failures and must never be
// reported as "validation complete".

type Order struct {
	Items []string `validate:"dive,required"`
}

type Node struct {
	Name string `validate:"required"`
	Next *Node
}

func printResult(title string, res validator.StructBudgetResult) {
	fmt.Printf("== %s ==\n", title)
	for _, fe := range res.Errors {
		fmt.Printf("  error: %s failed on '%s'\n", fe.Namespace(), fe.Tag())
	}
	fmt.Printf("  nodes visited: %d\n", res.NodesVisited)
	fmt.Printf("  stop reason:   %s\n", res.StopReason)
	if res.Truncated || res.StopReason != validator.BudgetStopNone {
		// The run stopped early: res.Errors is only what was collected
		// before the stop, NOT the total set of validation failures.
		fmt.Println("  result: PARTIAL - do not treat as a completed validation")
	} else if len(res.Errors) > 0 {
		fmt.Println("  result: COMPLETE - all validation failures are listed above")
	} else {
		fmt.Println("  result: COMPLETE - value is valid")
	}
	fmt.Println()
}

func main() {
	validate := validator.New()
	ctx := context.Background()

	// 1. No budget limits: behaves like StructCtx, everything is visited.
	one := 1
	res, _ := validate.StructWithBudget(ctx, Order{Items: []string{"", ""}}, validator.StructBudget{})
	printResult("unlimited budget, two failures", res)

	// 2. Error budget: stops right after the first error and is marked
	// truncated; the second empty item is never even looked at.
	res, _ = validate.StructWithBudget(ctx, Order{Items: []string{"", ""}}, validator.StructBudget{MaxErrors: &one})
	printResult("MaxErrors=1 (truncated)", res)

	// 3. Node budget: entering a struct field or dive element costs one
	// node; the root struct is free. Here: the Items field + one element.
	two := 2
	res, _ = validate.StructWithBudget(ctx, Order{Items: []string{"", ""}}, validator.StructBudget{MaxNodes: &two})
	printResult("MaxNodes=2", res)

	// 4. Context cancellation: the callback that cancels is allowed to
	// finish, but no further node is entered.
	ctx2, cancel := context.WithCancel(ctx)
	defer cancel()
	_ = validate.RegisterValidation("cancelme", func(fl validator.FieldLevel) bool {
		cancel()
		return true
	})
	type Pair struct {
		A string `validate:"cancelme"`
		B string `validate:"required"`
	}
	res, _ = validate.StructWithBudget(ctx2, Pair{B: "ok"}, validator.StructBudget{})
	printResult("context canceled by a custom validator", res)

	// 5. Cycles: the same pointer twice on the active path stops traversal.
	// A shared sub-object on different branches is NOT a cycle.
	self := &Node{Name: "self"}
	self.Next = self
	res, _ = validate.StructWithBudget(ctx, self, validator.StructBudget{})
	printResult("self-referencing pointer", res)
}
