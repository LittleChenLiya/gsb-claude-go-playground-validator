package validator

import (
	"strings"
	"testing"

	. "github.com/go-playground/assert/v2"
)

type uniqueTestRow struct {
	Tenant string
	Name   string
}

type uniqueTestStruct struct {
	Rows []uniqueTestRow
}

func TestStructLevelUniqueRealConflict(t *testing.T) {
	validate := New()

	err := validate.RegisterStructLevelUnique("Rows", []string{"Tenant", "Name"}, UniqueNilSkip, uniqueTestStruct{})
	Equal(t, err, nil)

	s := uniqueTestStruct{Rows: []uniqueTestRow{
		{Tenant: "a", Name: "b"},
		{Tenant: "a", Name: "b"},
		{Tenant: "a", Name: "c"},
	}}

	err = validate.Struct(s)
	NotEqual(t, err, nil)

	errs, ok := err.(ValidationErrors)
	Equal(t, ok, true)
	Equal(t, len(errs), 2)

	// both the first occurrence and the duplicate are reported
	Equal(t, errs[0].Tag(), "uniquefields")
	Equal(t, errs[0].Namespace(), "uniqueTestStruct.Rows[0]")
	Equal(t, errs[1].Tag(), "uniquefields")
	Equal(t, errs[1].Namespace(), "uniqueTestStruct.Rows[1]")
	Equal(t, errs[0].Param(), "Tenant,Name")

	// element order is untouched
	Equal(t, s.Rows[0].Name, "b")
	Equal(t, s.Rows[1].Name, "b")
	Equal(t, s.Rows[2].Name, "c")
}

func TestStructLevelUniqueNoStringConcatCollision(t *testing.T) {
	validate := New()

	err := validate.RegisterStructLevelUnique("Rows", []string{"Tenant", "Name"}, UniqueNilSkip, uniqueTestStruct{})
	Equal(t, err, nil)

	// naive string concatenation would produce "abc" for both rows
	s := uniqueTestStruct{Rows: []uniqueTestRow{
		{Tenant: "a", Name: "bc"},
		{Tenant: "ab", Name: "c"},
	}}

	Equal(t, validate.Struct(s), nil)
}

func TestStructLevelUniqueMultipleDuplicates(t *testing.T) {
	validate := New()

	err := validate.RegisterStructLevelUnique("Rows", []string{"Tenant", "Name"}, UniqueNilSkip, uniqueTestStruct{})
	Equal(t, err, nil)

	s := uniqueTestStruct{Rows: []uniqueTestRow{
		{Tenant: "a", Name: "b"},
		{Tenant: "a", Name: "b"},
		{Tenant: "a", Name: "b"},
	}}

	err = validate.Struct(s)
	NotEqual(t, err, nil)

	errs, ok := err.(ValidationErrors)
	Equal(t, ok, true)
	Equal(t, len(errs), 3)
	Equal(t, errs[0].Namespace(), "uniqueTestStruct.Rows[0]")
	Equal(t, errs[1].Namespace(), "uniqueTestStruct.Rows[1]")
	Equal(t, errs[2].Namespace(), "uniqueTestStruct.Rows[2]")
}

func TestStructLevelUniqueEmptyCollection(t *testing.T) {
	validate := New()

	err := validate.RegisterStructLevelUnique("Rows", []string{"Tenant", "Name"}, UniqueNilSkip, uniqueTestStruct{})
	Equal(t, err, nil)

	Equal(t, validate.Struct(uniqueTestStruct{}), nil)
	Equal(t, validate.Struct(uniqueTestStruct{Rows: []uniqueTestRow{}}), nil)
	Equal(t, validate.Struct(uniqueTestStruct{Rows: []uniqueTestRow{{Tenant: "a", Name: "b"}}}), nil)
}

type uniqueTenant string

type uniquePtrRow struct {
	Tenant *uniqueTenant
	Code   *int
	Active bool
}

type uniquePtrStruct struct {
	Rows []*uniquePtrRow
}

func TestStructLevelUniquePointersAndNamedTypes(t *testing.T) {
	validate := New()

	err := validate.RegisterStructLevelUnique("Rows", []string{"Tenant", "Code", "Active"}, UniqueNilSkip, uniquePtrStruct{})
	Equal(t, err, nil)

	tenantA := uniqueTenant("a")
	tenantB := uniqueTenant("b")
	code1 := 1
	code2 := 2

	// equal pointed-to values, distinct pointers: still duplicates
	s := uniquePtrStruct{Rows: []*uniquePtrRow{
		{Tenant: &tenantA, Code: &code1, Active: true},
		{Tenant: &tenantA, Code: &code1, Active: true},
		{Tenant: &tenantB, Code: &code1, Active: true},
		{Tenant: &tenantA, Code: &code2, Active: true},
		{Tenant: &tenantA, Code: &code1, Active: false},
		nil, // nil elements are skipped
	}}

	err = validate.Struct(s)
	NotEqual(t, err, nil)

	errs, ok := err.(ValidationErrors)
	Equal(t, ok, true)
	Equal(t, len(errs), 2)
	Equal(t, errs[0].Namespace(), "uniquePtrStruct.Rows[0]")
	Equal(t, errs[1].Namespace(), "uniquePtrStruct.Rows[1]")
}

func TestStructLevelUniqueNilSkip(t *testing.T) {
	validate := New()

	err := validate.RegisterStructLevelUnique("Rows", []string{"Tenant", "Code"}, UniqueNilSkip, uniquePtrStruct{})
	Equal(t, err, nil)

	code1 := 1

	// nil fields are skipped entirely, so no duplicates are reported
	s := uniquePtrStruct{Rows: []*uniquePtrRow{
		{Tenant: nil, Code: &code1},
		{Tenant: nil, Code: &code1},
	}}

	Equal(t, validate.Struct(s), nil)
}

func TestStructLevelUniqueNilAsValue(t *testing.T) {
	validate := New()

	err := validate.RegisterStructLevelUnique("Rows", []string{"Tenant", "Code"}, UniqueNilAsValue, uniquePtrStruct{})
	Equal(t, err, nil)

	tenantA := uniqueTenant("a")
	code1 := 1

	// nil participates as a value: two nils with the same Code are duplicates
	s := uniquePtrStruct{Rows: []*uniquePtrRow{
		{Tenant: nil, Code: &code1},
		{Tenant: nil, Code: &code1},
		{Tenant: &tenantA, Code: &code1},
	}}

	err = validate.Struct(s)
	NotEqual(t, err, nil)

	errs, ok := err.(ValidationErrors)
	Equal(t, ok, true)
	Equal(t, len(errs), 2)
	Equal(t, errs[0].Namespace(), "uniquePtrStruct.Rows[0]")
	Equal(t, errs[1].Namespace(), "uniquePtrStruct.Rows[1]")

	// nil and non-nil are distinct values
	s = uniquePtrStruct{Rows: []*uniquePtrRow{
		{Tenant: nil, Code: &code1},
		{Tenant: &tenantA, Code: &code1},
	}}

	Equal(t, validate.Struct(s), nil)
}

func TestStructLevelUniqueRegistrationErrors(t *testing.T) {
	type floatRow struct {
		Score float64
	}

	type floatStruct struct {
		Rows []floatRow
	}

	type unexportedRow struct {
		name string
	}

	type unexportedStruct struct {
		Rows []unexportedRow
	}

	type notACollection struct {
		Rows string
	}

	validate := New()

	// unsupported float type
	NotEqual(t, validate.RegisterStructLevelUnique("Rows", []string{"Score"}, UniqueNilSkip, floatStruct{}), nil)

	// missing field
	NotEqual(t, validate.RegisterStructLevelUnique("Rows", []string{"Nope"}, UniqueNilSkip, uniqueTestStruct{}), nil)

	// missing collection
	NotEqual(t, validate.RegisterStructLevelUnique("Nope", []string{"Tenant"}, UniqueNilSkip, uniqueTestStruct{}), nil)

	// collection is not a slice or array
	NotEqual(t, validate.RegisterStructLevelUnique("Rows", []string{"Tenant"}, UniqueNilSkip, notACollection{}), nil)

	// unexported field
	NotEqual(t, validate.RegisterStructLevelUnique("Rows", []string{"name"}, UniqueNilSkip, unexportedStruct{}), nil)

	// invalid policy
	NotEqual(t, validate.RegisterStructLevelUnique("Rows", []string{"Tenant"}, UniqueNilPolicy(42), uniqueTestStruct{}), nil)

	// empty collection path and field paths
	NotEqual(t, validate.RegisterStructLevelUnique("", []string{"Tenant"}, UniqueNilSkip, uniqueTestStruct{}), nil)
	NotEqual(t, validate.RegisterStructLevelUnique("Rows", nil, UniqueNilSkip, uniqueTestStruct{}), nil)

	// non struct type
	NotEqual(t, validate.RegisterStructLevelUnique("Rows", []string{"Tenant"}, UniqueNilSkip, "not a struct"), nil)
}

type uniqueInner struct {
	Items []uniqueTestRow
}

type uniqueOuter struct {
	Group  *uniqueInner
	Others []uniqueTestRow
}

func TestStructLevelUniqueNestedCollections(t *testing.T) {
	validate := New()

	err := validate.RegisterStructLevelUnique("Group.Items", []string{"Tenant", "Name"}, UniqueNilSkip, uniqueOuter{})
	Equal(t, err, nil)

	err = validate.RegisterStructLevelUnique("Others", []string{"Tenant"}, UniqueNilSkip, uniqueOuter{})
	Equal(t, err, nil)

	s := uniqueOuter{
		Group: &uniqueInner{Items: []uniqueTestRow{
			{Tenant: "a", Name: "b"},
			{Tenant: "a", Name: "b"},
		}},
		Others: []uniqueTestRow{
			{Tenant: "x", Name: "1"},
			{Tenant: "x", Name: "2"},
		},
	}

	err = validate.Struct(s)
	NotEqual(t, err, nil)

	errs, ok := err.(ValidationErrors)
	Equal(t, ok, true)
	Equal(t, len(errs), 4)
	Equal(t, errs[0].Namespace(), "uniqueOuter.Group.Items[0]")
	Equal(t, errs[1].Namespace(), "uniqueOuter.Group.Items[1]")
	Equal(t, errs[2].Namespace(), "uniqueOuter.Others[0]")
	Equal(t, errs[3].Namespace(), "uniqueOuter.Others[1]")

	// nil intermediate pointer: collection is simply not checked
	Equal(t, validate.Struct(uniqueOuter{}), nil)
}

type uniqueCoexistRow struct {
	Tenant string `validate:"required"`
	Name   string `validate:"required"`
}

type uniqueCoexistStruct struct {
	Label string             `validate:"required"`
	Rows  []uniqueCoexistRow `validate:"dive"`
}

func TestStructLevelUniqueCoexistsWithDiveAndFields(t *testing.T) {
	validate := New()

	err := validate.RegisterStructLevelUnique("Rows", []string{"Tenant", "Name"}, UniqueNilSkip, uniqueCoexistStruct{})
	Equal(t, err, nil)

	s := uniqueCoexistStruct{
		Rows: []uniqueCoexistRow{
			{Tenant: "a", Name: "b"},
			{Tenant: "a", Name: "b"},
			{Tenant: "", Name: "c"},
		},
	}

	err = validate.Struct(s)
	NotEqual(t, err, nil)

	errs, ok := err.(ValidationErrors)
	Equal(t, ok, true)

	var required, unique int
	for _, fe := range errs {
		switch fe.Tag() {
		case "required":
			required++
		case "uniquefields":
			unique++
		}
	}

	// Label required + Rows[2].Tenant required from dive + 2 uniquefields errors
	Equal(t, required, 2)
	Equal(t, unique, 2)
	Equal(t, len(errs), 4)
}

func TestStructLevelUniqueErrorContent(t *testing.T) {
	validate := New()

	err := validate.RegisterStructLevelUnique("Rows", []string{"Tenant", "Name"}, UniqueNilSkip, uniqueTestStruct{})
	Equal(t, err, nil)

	s := uniqueTestStruct{Rows: []uniqueTestRow{
		{Tenant: "secret-tenant", Name: "secret-name"},
		{Tenant: "secret-tenant", Name: "secret-name"},
	}}

	err = validate.Struct(s)
	NotEqual(t, err, nil)

	// the composite key contents must not leak into the error output
	msg := err.Error()
	Equal(t, strings.Contains(msg, "secret-tenant"), false)
	Equal(t, strings.Contains(msg, "secret-name"), false)
	Equal(t, strings.Contains(msg, "Rows[0]"), true)
	Equal(t, strings.Contains(msg, "Rows[1]"), true)

	errs, ok := err.(ValidationErrors)
	Equal(t, ok, true)
	for _, fe := range errs {
		Equal(t, fe.Value(), nil)
		Equal(t, fe.Param(), "Tenant,Name")
	}
}
