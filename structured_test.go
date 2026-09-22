package validator

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	. "github.com/go-playground/assert/v2"
)

func seNewValidate() *Validate {
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

func seFind(t *testing.T, se []StructuredError, fieldPath string) StructuredError {
	t.Helper()
	for _, e := range se {
		if e.FieldPath == fieldPath {
			return e
		}
	}
	t.Fatalf("structured error for field path %q not found in %#v", fieldPath, se)
	return StructuredError{}
}

type seItem struct {
	Name string `json:"name" validate:"required"`
}

type seRequest struct {
	Items  []seItem          `json:"items" validate:"required,dive"`
	Labels map[string]string `json:"labels" validate:"dive,required"`
	Counts map[int]string    `json:"counts" validate:"dive,required"`
}

func TestStructuredSliceMapPaths(t *testing.T) {
	v := seNewValidate()

	req := seRequest{
		Items:  []seItem{{Name: "ok"}, {Name: ""}},
		Labels: map[string]string{"a/b~c": ""},
		Counts: map[int]string{5: ""},
	}

	err := v.Struct(req)
	NotEqual(t, err, nil)

	errs, ok := err.(ValidationErrors)
	Equal(t, ok, true)

	se := errs.Structured()
	Equal(t, len(se), 3)

	// slice index: items[1].name
	item := seFind(t, se, "items[1].name")
	Equal(t, item.Namespace, "seRequest.Items[1].Name")
	Equal(t, item.Pointer, "/items/1/name")
	Equal(t, item.Tag, "required")
	Equal(t, item.ActualTag, "required")
	Equal(t, item.Unresolved, false)
	Equal(t, item.Value, nil) // values excluded by default

	// string map key: JSON Pointer escaping of '/' and '~'
	label := seFind(t, se, "labels[a/b~c]")
	Equal(t, label.Namespace, "seRequest.Labels[a/b~c]")
	Equal(t, label.Pointer, "/labels/a~1b~0c")

	// non-string map key: typed path segment, no JSON Pointer fabricated
	count := seFind(t, se, "counts[int(5)]")
	Equal(t, count.Namespace, "seRequest.Counts[5]")
	Equal(t, count.Pointer, "")

	// original error string format untouched
	Equal(t, strings.HasPrefix(errs[0].Error(), "Key: 'seRequest."), true)
}

func TestStructuredValuesOptIn(t *testing.T) {
	v := seNewValidate()

	req := seRequest{Items: []seItem{{Name: ""}}}
	err := v.Struct(req)
	NotEqual(t, err, nil)

	errs := err.(ValidationErrors)

	se := errs.Structured()
	Equal(t, se[0].Value, nil)

	se = errs.Structured(WithValues())
	NotEqual(t, se, nil)
	Equal(t, se[0].Value, "")
}

func TestStructuredNilPointer(t *testing.T) {
	v := seNewValidate()

	type seNilPtr struct {
		Nick *string `json:"nick" validate:"required"`
	}

	err := v.Struct(seNilPtr{})
	NotEqual(t, err, nil)

	se := err.(ValidationErrors).Structured()
	Equal(t, len(se), 1)
	Equal(t, se[0].Namespace, "seNilPtr.Nick")
	Equal(t, se[0].FieldPath, "nick")
	Equal(t, se[0].Pointer, "/nick")
	Equal(t, se[0].Tag, "required")
}

func TestStructuredNestedDive(t *testing.T) {
	v := seNewValidate()

	type seNested struct {
		Matrix [][]string `json:"matrix" validate:"dive,dive,required"`
	}

	err := v.Struct(seNested{Matrix: [][]string{{"ok", ""}}})
	NotEqual(t, err, nil)

	se := err.(ValidationErrors).Structured()
	Equal(t, len(se), 1)
	Equal(t, se[0].Namespace, "seNested.Matrix[0][1]")
	Equal(t, se[0].FieldPath, "matrix[0][1]")
	Equal(t, se[0].Pointer, "/matrix/0/1")
}

func TestStructuredMapKeysEndKeys(t *testing.T) {
	v := seNewValidate()

	type seKeys struct {
		Labels map[string]string `json:"labels" validate:"dive,keys,eq=ok,endkeys,required"`
	}

	err := v.Struct(seKeys{Labels: map[string]string{"a/b": "fine"}})
	NotEqual(t, err, nil)

	se := err.(ValidationErrors).Structured()
	Equal(t, len(se), 1)
	Equal(t, se[0].Namespace, "seKeys.Labels[a/b]")
	Equal(t, se[0].FieldPath, "labels[a/b]")
	Equal(t, se[0].Pointer, "/labels/a~1b")
	Equal(t, se[0].Tag, "eq")
	Equal(t, se[0].Param, "ok")
}

func TestStructuredDuplicateExternalNames(t *testing.T) {
	// a custom tag key is used here because go vet rejects duplicate json
	// tag names, which is exactly the collision this test exercises
	v := New()
	v.RegisterTagNameFunc(func(fld reflect.StructField) string {
		return fld.Tag.Get("ext")
	})

	type seDup struct {
		First  string `ext:"same" validate:"required"`
		Second string `ext:"same" validate:"required"`
	}

	err := v.Struct(seDup{})
	NotEqual(t, err, nil)

	errs := err.(ValidationErrors)
	Equal(t, len(errs), 2)

	se := errs.Structured()
	Equal(t, len(se), 2)
	// both share the external path, the Go namespace keeps them distinguishable
	Equal(t, se[0].FieldPath, "same")
	Equal(t, se[1].FieldPath, "same")
	Equal(t, se[0].Pointer, "/same")
	Equal(t, se[1].Pointer, "/same")
	NotEqual(t, se[0].Namespace, se[1].Namespace)
}

func TestStructuredFallbackToGoFieldName(t *testing.T) {
	v := seNewValidate()

	type seFallback struct {
		Internal string `validate:"required"` // no json tag, no external name
	}

	err := v.Struct(seFallback{})
	NotEqual(t, err, nil)

	se := err.(ValidationErrors).Structured()
	Equal(t, len(se), 1)
	Equal(t, se[0].FieldPath, "Internal")
	Equal(t, se[0].Pointer, "/Internal")
}

func TestStructuredAliasAndOr(t *testing.T) {
	v := seNewValidate()
	v.RegisterAlias("secolour", "hexcolor|rgb|rgba|hsl|hsla")

	type seAlias struct {
		Colour string `json:"colour" validate:"secolour"`
		Other  string `json:"other" validate:"rgb|rgba"`
	}

	err := v.Struct(seAlias{Colour: "nope", Other: "nope"})
	NotEqual(t, err, nil)

	se := err.(ValidationErrors).Structured()
	Equal(t, len(se), 2)

	// alias: original failed constraint preserved
	alias := seFind(t, se, "colour")
	Equal(t, alias.Tag, "secolour")
	Equal(t, alias.ActualTag, "hexcolor|rgb|rgba|hsl|hsla")
	Equal(t, alias.Pointer, "/colour")

	// or combination: entire failed combination preserved
	or := seFind(t, se, "other")
	Equal(t, or.Tag, "rgb|rgba")
	Equal(t, or.ActualTag, "rgb|rgba")
}

type seStructLevel struct {
	Name string `json:"name"`
}

func seStructLevelValidation(sl StructLevel) {
	u := sl.Current().Interface().(seStructLevel)

	// provable: "Name" is a real field of the struct
	if u.Name == "bad" {
		sl.ReportError(u.Name, "Name", "Name", "namepolicy", "")
	}

	// unprovable custom path
	if u.Name == "worse" {
		sl.ReportError(u.Name, "Custom[0].Path", "Custom[0].Path", "custom", "")
	}
}

func TestStructuredStructLevel(t *testing.T) {
	v := seNewValidate()
	v.RegisterStructValidation(seStructLevelValidation, seStructLevel{})

	// resolved struct level error
	err := v.Struct(seStructLevel{Name: "bad"})
	NotEqual(t, err, nil)
	se := err.(ValidationErrors).Structured()
	Equal(t, len(se), 1)
	Equal(t, se[0].Unresolved, false)
	Equal(t, se[0].Namespace, "seStructLevel.Name")
	Equal(t, se[0].FieldPath, "name")
	Equal(t, se[0].Pointer, "/name")
	Equal(t, se[0].Tag, "namepolicy")

	// unresolved custom path: original namespace kept, no pointer fabricated
	err = v.Struct(seStructLevel{Name: "worse"})
	NotEqual(t, err, nil)
	se = err.(ValidationErrors).Structured()
	Equal(t, len(se), 1)
	Equal(t, se[0].Unresolved, true)
	Equal(t, se[0].Namespace, "seStructLevel.Custom[0].Path")
	Equal(t, se[0].FieldPath, "seStructLevel.Custom[0].Path")
	Equal(t, se[0].Pointer, "")
	Equal(t, se[0].Tag, "custom")
}

func TestStructuredCompatibility(t *testing.T) {
	v := seNewValidate()

	type seCompat struct {
		Email string `json:"email" validate:"required,email"`
	}

	err := v.Struct(seCompat{})
	NotEqual(t, err, nil)

	// original ValidationErrors type + error string unchanged
	errs, ok := err.(ValidationErrors)
	Equal(t, ok, true)
	Equal(t, errs.Error(), "Key: 'seCompat.email' Error:Field validation for 'email' failed on the 'required' tag")

	// FieldError interface intact
	var fe FieldError = errs[0]
	Equal(t, fe.Tag(), "required")
	Equal(t, fe.Namespace(), "seCompat.email")
	Equal(t, fe.StructNamespace(), "seCompat.Email")
	Equal(t, fe.Field(), "email")
	Equal(t, fe.StructField(), "Email")

	// structured conversion is JSON serializable
	out, jerr := json.Marshal(errs.Structured())
	Equal(t, jerr, nil)
	Equal(t, strings.Contains(string(out), `"pointer":"/email"`), true)
	Equal(t, strings.Contains(string(out), `"value"`), false)
}
