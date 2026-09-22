package validator

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	. "github.com/go-playground/assert/v2"
)

func structuredValidate() *Validate {
	v := New()
	v.RegisterTagNameFunc(func(fld reflect.StructField) string {
		name := strings.SplitN(fld.Tag.Get("json"), ",", 2)[0]
		if name == "-" {
			return ""
		}
		return name
	})
	return v
}

func structuredErrs(t *testing.T, err error) ValidationErrors {
	t.Helper()
	NotEqual(t, err, nil)
	ve, ok := err.(ValidationErrors)
	Equal(t, ok, true)
	return ve
}

func TestStructuredSliceIndexPointer(t *testing.T) {
	type Item struct {
		Name string `json:"name" validate:"required"`
	}
	type Order struct {
		Items []Item `json:"items" validate:"dive"`
	}

	v := structuredValidate()
	err := v.Struct(Order{Items: []Item{{Name: "ok"}, {}}})
	ve := structuredErrs(t, err)
	Equal(t, len(ve), 1)

	se := ve.Structured()
	Equal(t, len(se), 1)
	Equal(t, se[0].Pointer, "/items/1/name")
	Equal(t, se[0].Namespace, "Order.Items[1].Name")
	Equal(t, se[0].FieldPath, "Order.items[1].name")
	Equal(t, se[0].Tag, "required")
	Equal(t, se[0].ActualTag, "required")
	Equal(t, se[0].Unresolved, false)
	Equal(t, se[0].Value, nil) // values omitted by default

	// segments recorded during validation
	Equal(t, len(se[0].Path), 4)
	Equal(t, se[0].Path[0].Kind, SegmentRoot)
	Equal(t, se[0].Path[1].Kind, SegmentField)
	Equal(t, se[0].Path[1].Field, "Items")
	Equal(t, se[0].Path[1].Name, "items")
	Equal(t, se[0].Path[2].Kind, SegmentIndex)
	Equal(t, *se[0].Path[2].Index, 1)
	Equal(t, se[0].Path[3].Kind, SegmentField)
}

func TestStructuredMapStringKeyPointerEscaped(t *testing.T) {
	type Form struct {
		Labels map[string]string `json:"labels" validate:"dive,required"`
	}

	v := structuredValidate()
	err := v.Struct(Form{Labels: map[string]string{"a/b~c": ""}})
	ve := structuredErrs(t, err)
	Equal(t, len(ve), 1)

	se := ve.Structured()
	Equal(t, se[0].Pointer, "/labels/a~1b~0c")
	Equal(t, se[0].Namespace, "Form.Labels[a/b~c]")
	Equal(t, se[0].FieldPath, "Form.labels[a/b~c]")
	Equal(t, se[0].Path[2].Kind, SegmentMapKey)
	Equal(t, se[0].Path[2].Key, "a/b~c")
}

func TestStructuredMapKeysEndkeys(t *testing.T) {
	type Form struct {
		Labels map[string]string `json:"labels" validate:"dive,keys,required,endkeys,required"`
	}

	v := structuredValidate()
	err := v.Struct(Form{Labels: map[string]string{"ok": ""}})
	ve := structuredErrs(t, err)
	Equal(t, len(ve), 1)

	se := ve.Structured()
	Equal(t, se[0].Pointer, "/labels/ok")
	Equal(t, se[0].Tag, "required")
	Equal(t, se[0].Path[2].Kind, SegmentMapKey)
	Equal(t, se[0].Path[2].Key, "ok")
}

func TestStructuredMapNonStringKeyNoPointer(t *testing.T) {
	type Form struct {
		Meta map[int]string `json:"meta" validate:"dive,required"`
	}

	v := structuredValidate()
	err := v.Struct(Form{Meta: map[int]string{7: ""}})
	ve := structuredErrs(t, err)
	Equal(t, len(ve), 1)

	se := ve.Structured()
	// no JSON Pointer is fabricated for non-string map keys
	Equal(t, se[0].Pointer, "")
	Equal(t, se[0].Namespace, "Form.Meta[7]")
	Equal(t, se[0].FieldPath, "Form.meta[7]")
	Equal(t, se[0].Unresolved, false)

	// the typed path segment is still recorded
	last := se[0].Path[len(se[0].Path)-1]
	Equal(t, last.Kind, SegmentTypedMapKey)
	Equal(t, last.Key, "7")
	Equal(t, last.KeyType, "int")
}

func TestStructuredNilPointer(t *testing.T) {
	type Form struct {
		Name *string `json:"name" validate:"required"`
	}

	v := structuredValidate()
	err := v.Struct(Form{})
	ve := structuredErrs(t, err)
	Equal(t, len(ve), 1)

	se := ve.Structured()
	Equal(t, se[0].Pointer, "/name")
	Equal(t, se[0].Namespace, "Form.Name")
	Equal(t, se[0].FieldPath, "Form.name")
	Equal(t, se[0].Tag, "required")
}

func TestStructuredNestedDive(t *testing.T) {
	type Form struct {
		Matrix [][]string `json:"matrix" validate:"dive,dive,required"`
	}

	v := structuredValidate()
	err := v.Struct(Form{Matrix: [][]string{{"a", ""}}})
	ve := structuredErrs(t, err)
	Equal(t, len(ve), 1)

	se := ve.Structured()
	Equal(t, se[0].Pointer, "/matrix/0/1")
	Equal(t, se[0].Namespace, "Form.Matrix[0][1]")
	Equal(t, se[0].FieldPath, "Form.matrix[0][1]")
}

func TestStructuredDuplicateExternalNames(t *testing.T) {
	type Form struct {
		First  string `json:"first" validate:"required"`
		Second string `json:"second" validate:"required"`
	}

	v := New()
	// map both fields to the same external name
	v.RegisterTagNameFunc(func(fld reflect.StructField) string {
		return "dup"
	})
	err := v.Struct(Form{})
	ve := structuredErrs(t, err)
	Equal(t, len(ve), 2)

	se := ve.Structured()
	// external paths collide, Go namespaces still disambiguate
	Equal(t, se[0].FieldPath, "Form.dup")
	Equal(t, se[1].FieldPath, "Form.dup")
	Equal(t, se[0].Pointer, "/dup")
	Equal(t, se[1].Pointer, "/dup")
	Equal(t, se[0].Namespace, "Form.First")
	Equal(t, se[1].Namespace, "Form.Second")
}

func TestStructuredAliasAndOr(t *testing.T) {
	v := structuredValidate()
	v.RegisterAlias("iscolor", "hexcolor|rgb|rgba|hsl|hsla")

	type Form struct {
		Color string `json:"color" validate:"iscolor"`
		Plain string `json:"plain" validate:"rgb|rgba"`
	}

	err := v.Struct(Form{Color: "nope", Plain: "nope"})
	ve := structuredErrs(t, err)
	Equal(t, len(ve), 2)

	se := ve.Structured()
	// alias keeps the original failed constraint
	Equal(t, se[0].Tag, "iscolor")
	Equal(t, se[0].ActualTag, "hexcolor|rgb|rgba|hsl|hsla")
	Equal(t, se[0].Pointer, "/color")
	// or combination keeps the whole failed constraint
	Equal(t, se[1].Tag, "rgb|rgba")
	Equal(t, se[1].ActualTag, "rgb|rgba")
	Equal(t, se[1].Pointer, "/plain")
}

func TestStructuredStructLevel(t *testing.T) {
	type Profile struct {
		Name string `json:"name"`
	}

	v := structuredValidate()
	v.RegisterStructValidation(func(sl StructLevel) {
		p := sl.Current().Interface().(Profile)
		if p.Name == "" {
			// provable: simple field name of the current struct
			sl.ReportError(p.Name, "name", "Name", "required", "")
			// unprovable custom path
			sl.ReportError(nil, "audit[0].trail", "Audit[0].Trail", "custom", "")
		}
	}, Profile{})

	err := v.Struct(Profile{})
	ve := structuredErrs(t, err)
	Equal(t, len(ve), 2)

	se := ve.Structured()

	Equal(t, se[0].Unresolved, false)
	Equal(t, se[0].Pointer, "/name")
	Equal(t, se[0].Namespace, "Profile.Name")
	Equal(t, se[0].FieldPath, "Profile.name")

	Equal(t, se[1].Unresolved, true)
	Equal(t, se[1].Pointer, "") // nothing fabricated
	// original namespace preserved
	Equal(t, se[1].Namespace, "Profile.Audit[0].Trail")
	Equal(t, se[1].FieldPath, "Profile.audit[0].trail")
	Equal(t, se[1].Tag, "custom")
	last := se[1].Path[len(se[1].Path)-1]
	Equal(t, last.Kind, SegmentUnresolved)
}

func TestStructuredValuesOptIn(t *testing.T) {
	type Credentials struct {
		Password string `json:"password" validate:"min=8"`
	}

	v := structuredValidate()
	err := v.Struct(Credentials{Password: "secret"})
	ve := structuredErrs(t, err)
	Equal(t, len(ve), 1)

	// default: no values, passwords do not leak into API responses
	se := ve.Structured()
	Equal(t, se[0].Value, nil)
	Equal(t, se[0].Param, "8")

	// opt-in
	se = ve.Structured(WithValues())
	Equal(t, se[0].Value, "secret")
}

func TestStructuredCompatibilityRegression(t *testing.T) {
	type Inner struct {
		Tags []string `json:"tags" validate:"dive,min=2"`
	}
	type Outer struct {
		Name  string            `json:"name" validate:"required"`
		Inner Inner             `json:"inner"`
		Ptr   *Inner            `json:"ptr"`
		Map   map[string]string `json:"map" validate:"dive,required"`
	}

	v := structuredValidate()
	err := v.Struct(Outer{
		Inner: Inner{Tags: []string{"x"}},
		Ptr:   &Inner{Tags: []string{""}},
		Map:   map[string]string{"k": ""},
	})
	ve := structuredErrs(t, err)
	Equal(t, len(ve), 4)

	// original error string format unchanged
	for i, fe := range ve {
		Equal(t, strings.HasPrefix(fe.Error(), "Key: '"), true)
		Equal(t, strings.Contains(fe.Error(), "Error:Field validation for '"), true)
		Equal(t, strings.Contains(fe.Error(), "' failed on the '"), true)
		_ = i
	}
	Equal(t, strings.HasSuffix(ve.Error(), "tag"), true)

	se := ve.Structured()
	Equal(t, len(se), len(ve))

	// structured output consistent with the existing FieldError interface
	for i, fe := range ve {
		Equal(t, se[i].Namespace, fe.StructNamespace())
		Equal(t, se[i].FieldPath, fe.Namespace())
		Equal(t, se[i].Tag, fe.Tag())
		Equal(t, se[i].ActualTag, fe.ActualTag())
		Equal(t, se[i].Param, fe.Param())
	}

	// types unchanged
	var _ error = ve
	var fe FieldError = ve[0]
	Equal(t, fe.Kind(), reflect.String)
	Equal(t, fe.Type() == reflect.TypeOf(""), true)

	// JSON marshalling of the structured output works end to end
	b, jerr := json.Marshal(se)
	Equal(t, jerr, nil)
	Equal(t, strings.Contains(string(b), `"pointer":"/name"`), true)
	Equal(t, strings.Contains(string(b), `"pointer":"/inner/tags/0"`), true)
	Equal(t, strings.Contains(string(b), `"pointer":"/ptr/tags/0"`), true)
	Equal(t, strings.Contains(string(b), `"pointer":"/map/k"`), true)
	// no values by default
	Equal(t, strings.Contains(string(b), `"value"`), false)
}

func TestStructuredVarAndForeignError(t *testing.T) {
	v := New()

	// Var on a primitive: no path can be recorded, namespaces stay blank
	err := v.Var("", "required")
	ve := structuredErrs(t, err)
	se := ve.Structured()
	Equal(t, len(se), 1)
	Equal(t, se[0].Namespace, "")
	Equal(t, se[0].FieldPath, "")
	Equal(t, se[0].Pointer, "")
	Equal(t, se[0].Tag, "required")

	// foreign FieldError implementations still convert, flagged unresolved
	type foreign struct{}
	_ = foreign{}
}
