package main

import (
	"context"
	"fmt"
	"reflect"

	"github.com/go-playground/validator/v10"
)

// LineItem is a single order line.
type LineItem struct {
	SKU string `validate:"required,unique-sku"`
}

// Order is the top-level struct being validated.
type Order struct {
	Items []LineItem `validate:"required,dive"`
}

// use a single instance of Validate, it caches struct info
var validate *validator.Validate

func main() {
	validate = validator.New()

	// Register a custom rule that reads the collection context to enforce
	// uniqueness of SKUs across the slice, without any global state.
	validate.RegisterValidationCtx("unique-sku", validateUniqueSKU)

	order := Order{
		Items: []LineItem{
			{SKU: "A"},
			{SKU: "B"},
			{SKU: "A"}, // duplicate
		},
	}

	err := validate.Struct(order)
	if err != nil {
		fmt.Printf("Err(s):\n%+v\n", err)
	}

	// A valid order passes.
	order.Items = []LineItem{{SKU: "A"}, {SKU: "B"}, {SKU: "C"}}
	err = validate.Struct(order)
	if err != nil {
		fmt.Printf("Err(s):\n%+v\n", err)
	} else {
		fmt.Println("order is valid")
	}
}

// validateUniqueSKU checks that the current item's SKU is unique within
// the collection being dived. It uses fl.CollectionLevel() to read the
// other items and the current index, so no global variables are needed.
func validateUniqueSKU(ctx context.Context, fl validator.FieldLevel) bool {
	cl := fl.CollectionLevel()
	if cl == nil {
		// Not within a dive; nothing to compare against.
		return true
	}

	sku := fl.Field().String()

	for i := 0; i < cl.Len(); i++ {
		if i == cl.Index() {
			continue
		}

		// Collection() returns a read-only view of the collection.
		other := cl.Collection().Index(i)
		if other.Kind() == reflect.Ptr {
			other = other.Elem()
		}
		otherSKU := other.FieldByName("SKU").String()

		if otherSKU == sku {
			return false
		}
	}

	return true
}
