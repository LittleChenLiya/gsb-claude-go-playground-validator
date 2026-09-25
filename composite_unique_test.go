package validator

import (
	"reflect"
	"strings"
	"testing"

	. "github.com/go-playground/assert/v2"
	"github.com/go-playground/locales/en"
	ut "github.com/go-playground/universal-translator"
)

type cuLine struct {
	Tenant string
	Name   string
}

type cuForm struct {
	Lines []cuLine
}

func TestCompositeUniqueBasic(t *testing.T) {
	validate := New()
	err := validate.RegisterCompositeUnique(cuForm{}, "Lines", []string{"Tenant", "Name"})
	if err != nil {
		t.Fatalf("registration failed: %s", err)
	}

	// two identical rows must produce errors for BOTH indexes
	form := cuForm{Lines: []cuLine{
		{Tenant: "a", Name: "b"},
		{Tenant: "x", Name: "y"},
		{Tenant: "a", Name: "b"},
	}}

	err = validate.Struct(form)
	NotEqual(t, err, nil)

	errs, ok := err.(ValidationErrors)
	if !ok {
		t.Fatalf("expected ValidationErrors, got %T", err)
	}
	Equal(t, len(errs), 2)
	Equal(t, errs[0].Namespace(), "cuForm.Lines[0]")
	Equal(t, errs[1].Namespace(), "cuForm.Lines[2]")
	Equal(t, errs[0].Tag(), compositeUniqueTag)
	Equal(t, errs[1].Tag(), compositeUniqueTag)
	Equal(t, errs[0].Param(), "Tenant,Name")
	Equal(t, errs[0].Value(), nil)

	// the composite key contents must not leak into the error string
	for _, fe := range errs {
		if strings.Contains(fe.Error(), "a,b") || strings.Contains(fe.Error(), "tenant=a") {
			t.Fatalf("error string leaks composite key contents: %s", fe.Error())
		}
	}

	// no duplicates -> valid
	err = validate.Struct(cuForm{Lines: []cuLine{
		{Tenant: "a", Name: "b"},
		{Tenant: "a", Name: "c"},
		{Tenant: "b", Name: "b"},
	}})
	Equal(t, err, nil)
}

func TestCompositeUniqueNoConcatCollision(t *testing.T) {
	validate := New()
	err := validate.RegisterCompositeUnique(cuForm{}, "Lines", []string{"Tenant", "Name"})
	if err != nil {
		t.Fatalf("registration failed: %s", err)
	}

	// ("a", "bc") vs ("ab", "c") would collide under naive string
	// concatenation keys ("a"+"bc" == "ab"+"c") and must NOT be flagged
	err = validate.Struct(cuForm{Lines: []cuLine{
		{Tenant: "a", Name: "bc"},
		{Tenant: "ab", Name: "c"},
	}})
	Equal(t, err, nil)
}

type cuPtrLine struct {
	Tenant *string
	Name   *string
}

type cuPtrForm struct {
	Lines []*cuPtrLine
}

func cuStr(s string) *string { return &s }

func TestCompositeUniqueNilPolicies(t *testing.T) {
	build := func() cuPtrForm {
		return cuPtrForm{Lines: []*cuPtrLine{
			{Tenant: nil, Name: cuStr("same")},
			{Tenant: cuStr("a"), Name: cuStr("same")},
			{Tenant: nil, Name: cuStr("same")},
		}}
	}

	// skip policy: elements with nil key fields never participate
	validate := New()
	err := validate.RegisterCompositeUnique(cuPtrForm{}, "Lines", []string{"Tenant", "Name"},
		WithCompositeUniqueNilPolicy(CompositeUniqueSkipNil))
	if err != nil {
		t.Fatalf("registration failed: %s", err)
	}
	Equal(t, validate.Struct(build()), nil)

	// nil-as-value policy: two nils in the same position are duplicates
	validate = New()
	err = validate.RegisterCompositeUnique(cuPtrForm{}, "Lines", []string{"Tenant", "Name"},
		WithCompositeUniqueNilPolicy(CompositeUniqueNilAsValue))
	if err != nil {
		t.Fatalf("registration failed: %s", err)
	}
	err = validate.Struct(build())
	NotEqual(t, err, nil)

	errs := err.(ValidationErrors)
	Equal(t, len(errs), 2)
	Equal(t, errs[0].Namespace(), "cuPtrForm.Lines[0]")
	Equal(t, errs[1].Namespace(), "cuPtrForm.Lines[2]")

	// nil vs non-nil are distinct values under nil-as-value
	err = validate.Struct(cuPtrForm{Lines: []*cuPtrLine{
		{Tenant: nil, Name: cuStr("same")},
		{Tenant: cuStr("a"), Name: cuStr("same")},
	}})
	Equal(t, err, nil)
}

type cuTenant string
type cuCode int32
type cuFlag bool

type cuNamedLine struct {
	Tenant cuTenant
	Code   cuCode
	Active cuFlag
}

type cuNamedForm struct {
	Lines []cuNamedLine
}

func TestCompositeUniqueNamedTypes(t *testing.T) {
	validate := New()
	err := validate.RegisterCompositeUnique(cuNamedForm{}, "Lines", []string{"Tenant", "Code", "Active"})
	if err != nil {
		t.Fatalf("registration failed: %s", err)
	}

	err = validate.Struct(cuNamedForm{Lines: []cuNamedLine{
		{Tenant: "a", Code: 1, Active: true},
		{Tenant: "a", Code: 1, Active: false},
		{Tenant: "a", Code: 2, Active: true},
		{Tenant: "a", Code: 1, Active: true},
	}})
	NotEqual(t, err, nil)

	errs := err.(ValidationErrors)
	Equal(t, len(errs), 2)
	Equal(t, errs[0].Namespace(), "cuNamedForm.Lines[0]")
	Equal(t, errs[1].Namespace(), "cuNamedForm.Lines[3]")
}

func TestCompositeUniqueEmptyCollection(t *testing.T) {
	validate := New()
	err := validate.RegisterCompositeUnique(cuForm{}, "Lines", []string{"Tenant", "Name"})
	if err != nil {
		t.Fatalf("registration failed: %s", err)
	}

	Equal(t, validate.Struct(cuForm{}), nil)
	Equal(t, validate.Struct(cuForm{Lines: []cuLine{}}), nil)
	Equal(t, validate.Struct(cuForm{Lines: []cuLine{{Tenant: "a", Name: "b"}}}), nil)
}

func TestCompositeUniqueRegistrationErrors(t *testing.T) {
	type floatLine struct {
		Score float64
	}
	type floatForm struct {
		Lines []floatLine
	}
	type notSlice struct {
		Lines string
	}
	type scalarElems struct {
		Lines []string
	}

	validate := New()

	// unsupported field type: float
	NotEqual(t, validate.RegisterCompositeUnique(floatForm{}, "Lines", []string{"Score"}), nil)

	// missing key field
	NotEqual(t, validate.RegisterCompositeUnique(cuForm{}, "Lines", []string{"Missing"}), nil)

	// missing collection
	NotEqual(t, validate.RegisterCompositeUnique(cuForm{}, "Nope", []string{"Tenant"}), nil)

	// collection is not a slice/array
	NotEqual(t, validate.RegisterCompositeUnique(notSlice{}, "Lines", []string{"Tenant"}), nil)

	// collection elements are not structs
	NotEqual(t, validate.RegisterCompositeUnique(scalarElems{}, "Lines", []string{"Tenant"}), nil)

	// no field paths
	NotEqual(t, validate.RegisterCompositeUnique(cuForm{}, "Lines", nil), nil)

	// nil sample
	NotEqual(t, validate.RegisterCompositeUnique(nil, "Lines", []string{"Tenant"}), nil)

	// non-struct sample
	NotEqual(t, validate.RegisterCompositeUnique("nope", "Lines", []string{"Tenant"}), nil)
}

type cuAddress struct {
	City *string
}

type cuNestedLine struct {
	Addr *cuAddress
	Name string
}

type cuNestedForm struct {
	Lines []cuNestedLine
}

func TestCompositeUniqueNestedFieldPath(t *testing.T) {
	validate := New()
	err := validate.RegisterCompositeUnique(cuNestedForm{}, "Lines", []string{"Addr.City", "Name"})
	if err != nil {
		t.Fatalf("registration failed: %s", err)
	}

	err = validate.Struct(cuNestedForm{Lines: []cuNestedLine{
		{Addr: &cuAddress{City: cuStr("sz")}, Name: "n1"},
		{Addr: &cuAddress{City: cuStr("sz")}, Name: "n1"},
	}})
	NotEqual(t, err, nil)
	Equal(t, len(err.(ValidationErrors)), 2)

	// nil intermediate pointer is skipped under the default policy
	err = validate.Struct(cuNestedForm{Lines: []cuNestedLine{
		{Addr: nil, Name: "n1"},
		{Addr: nil, Name: "n1"},
	}})
	Equal(t, err, nil)
}

type cuMember struct {
	Tenant string `validate:"required"`
	Name   string `validate:"required"`
}

type cuDept struct {
	Code    string     `validate:"required"`
	Members []cuMember `validate:"dive"`
}

type cuOrg struct {
	Depts []cuDept `validate:"required,dive"`
}

func TestCompositeUniqueNestedCollectionsAndCoexistence(t *testing.T) {
	validate := New()

	// rule on the root collection
	if err := validate.RegisterCompositeUnique(cuOrg{}, "Depts", []string{"Code"}); err != nil {
		t.Fatalf("registration failed: %s", err)
	}
	// rule on a nested collection, fires once per Dept element
	if err := validate.RegisterCompositeUnique(cuDept{}, "Members", []string{"Tenant", "Name"}); err != nil {
		t.Fatalf("registration failed: %s", err)
	}

	org := cuOrg{Depts: []cuDept{
		{Code: "d1", Members: []cuMember{
			{Tenant: "a", Name: "b"},
			{Tenant: "a", Name: "b"},
		}},
		{Code: "d1", Members: []cuMember{
			{Tenant: "a", Name: "b"}, // same tuple, different collection: fine
			{Tenant: "", Name: "x"},  // violates required, coexists with the rule
		}},
	}}

	err := validate.Struct(org)
	NotEqual(t, err, nil)

	errs := err.(ValidationErrors)
	ns := make(map[string]string, len(errs))
	for _, fe := range errs {
		ns[fe.Namespace()] = fe.Tag()
	}

	// duplicate dept codes on the root collection
	Equal(t, ns["cuOrg.Depts[0]"], compositeUniqueTag)
	Equal(t, ns["cuOrg.Depts[1]"], compositeUniqueTag)
	// duplicate members within the first dept
	Equal(t, ns["cuOrg.Depts[0].Members[0]"], compositeUniqueTag)
	Equal(t, ns["cuOrg.Depts[0].Members[1]"], compositeUniqueTag)
	// normal field validation via dive still runs
	Equal(t, ns["cuOrg.Depts[1].Members[1].Tenant"], "required")
	// same tuple in a different collection is not a duplicate
	if _, ok := ns["cuOrg.Depts[1].Members[0]"]; ok {
		t.Fatalf("unexpected duplicate across collections: %v", ns)
	}
}

func TestCompositeUniqueDoesNotReorderElements(t *testing.T) {
	validate := New()
	if err := validate.RegisterCompositeUnique(cuForm{}, "Lines", []string{"Tenant", "Name"}); err != nil {
		t.Fatalf("registration failed: %s", err)
	}

	form := cuForm{Lines: []cuLine{
		{Tenant: "a", Name: "b"},
		{Tenant: "c", Name: "d"},
		{Tenant: "a", Name: "b"},
	}}
	before := make([]cuLine, len(form.Lines))
	copy(before, form.Lines)

	NotEqual(t, validate.Struct(form), nil)
	if !reflect.DeepEqual(before, form.Lines) {
		t.Fatalf("element order changed: %v != %v", before, form.Lines)
	}
}

func TestCompositeUniqueTranslation(t *testing.T) {
	validate := New()
	if err := validate.RegisterCompositeUnique(cuForm{}, "Lines", []string{"Tenant", "Name"}); err != nil {
		t.Fatalf("registration failed: %s", err)
	}

	enLocale := en.New()
	uni := ut.New(enLocale, enLocale)
	trans, _ := uni.GetTranslator("en")

	err := validate.RegisterTranslation(compositeUniqueTag, trans,
		func(ut ut.Translator) error {
			return ut.Add(compositeUniqueTag, "{0} duplicates another entry on fields {1}", true)
		},
		func(ut ut.Translator, fe FieldError) string {
			t, err := ut.T(fe.Tag(), fe.Field(), fe.Param())
			if err != nil {
				return fe.(error).Error()
			}
			return t
		})
	if err != nil {
		t.Fatalf("translation registration failed: %s", err)
	}

	err = validate.Struct(cuForm{Lines: []cuLine{
		{Tenant: "a", Name: "b"},
		{Tenant: "a", Name: "b"},
	}})
	NotEqual(t, err, nil)

	translations := err.(ValidationErrors).Translate(trans)
	Equal(t, translations["cuForm.Lines[0]"], "Lines[0] duplicates another entry on fields Tenant,Name")
	Equal(t, translations["cuForm.Lines[1]"], "Lines[1] duplicates another entry on fields Tenant,Name")
}
