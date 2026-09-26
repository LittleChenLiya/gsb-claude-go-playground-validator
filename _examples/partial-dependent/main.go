package main

import (
	"fmt"

	"github.com/go-playground/validator/v10"
)

// DateRange models a form where End must stay after Start. Editing only
// Start can therefore invalidate End, a reverse dependency that
// StructPartialDependent discovers from the gtfield tag.
type DateRange struct {
	Start int    `validate:"gte=0"`
	End   int    `validate:"gtfield=Start"`
	Name  string `validate:"required"`
}

func main() {
	validate := validator.New()

	// the user only modified Start, pushing it past End.
	changed := DateRange{Start: 10, End: 5}

	// partial, dependency aware validation: only Start and the fields
	// depending on it (End) are validated, Name is left alone.
	res, err := validate.StructPartialDependent(changed, "Start")
	if res.RequiresFullValidation {
		// dependencies could not be determined statically, fall back to a
		// full validation instead of silently skipping checks.
		err = validate.Struct(changed)
	}
	fmt.Println("validated fields:", res.ValidatedFields)
	printErrs("partial", err)

	// full validation for comparison: it also reports the unrelated,
	// still empty Name field.
	printErrs("full", validate.Struct(changed))
}

func printErrs(prefix string, err error) {
	if err == nil {
		fmt.Println(prefix+":", "no errors")
		return
	}
	for _, fe := range err.(validator.ValidationErrors) {
		fmt.Printf("%s: field %s failed on the %s tag\n", prefix, fe.Field(), fe.Tag())
	}
}

// Output:
// validated fields: [End Start]
// partial: field End failed on the gtfield tag
// full: field End failed on the gtfield tag
// full: field Name failed on the required tag
