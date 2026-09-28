package validator

import (
	"reflect"
	"testing"

	"github.com/go-playground/assert/v2"
)

type namedDeepInner struct {
	City string `json:"city" cn:"城市" validate:"required"`
}

type namedDeepAddress struct {
	namedDeepInner
	Labels [][]string `json:"labels" cn:"标签" validate:"dive,dive,required"`
}

type namedDeepUser struct {
	Address *namedDeepAddress `json:"address" cn:"地址" validate:"required"`
}

// TestStructNamedFieldInfo verifies the callback receives the declaring
// struct, index and walked Go path, including anonymous embedded structs.
func TestStructNamedFieldInfo(t *testing.T) {
	v := New()
	user := namedDeepUser{
		Address: &namedDeepAddress{
			namedDeepInner: namedDeepInner{},
			Labels:         [][]string{{""}},
		},
	}

	type observed struct {
		structName string
		fieldName  string
		index      int
		path       string
	}
	var seen []observed

	_ = v.StructNamed(user, func(info FieldInfo) string {
		seen = append(seen, observed{
			structName: info.Struct.Name(),
			fieldName:  info.Field.Name,
			index:      info.Index,
			path:       info.Path,
		})
		return chineseFieldName(info)
	})

	find := func(field string) observed {
		for _, o := range seen {
			if o.fieldName == field {
				return o
			}
		}
		t.Fatalf("field %s not observed: %+v", field, seen)
		return observed{}
	}

	addr := find("Address")
	assert.Equal(t, addr.structName, "namedDeepUser")
	assert.Equal(t, addr.path, "namedDeepUser.Address")
	assert.Equal(t, addr.index, 0)

	city := find("City")
	assert.Equal(t, city.structName, "namedDeepInner")
	assert.Equal(t, city.path, "namedDeepUser.Address.namedDeepInner.City")
	assert.Equal(t, city.index, 0)
	assert.Equal(t, city.fieldName, "City")
}

// TestStructNamedNestedDiveAndPointer verifies indexes at multiple dive levels
// survive together with pointer traversal.
func TestStructNamedNestedDiveAndPointer(t *testing.T) {
	v := New()
	user := namedDeepUser{
		Address: &namedDeepAddress{
			Labels: [][]string{{""}, {"ok", ""}},
		},
	}

	errs := v.StructNamed(user, chineseFieldName).(ValidationErrors)
	fe := findNamedError(t, errs, "namedDeepUser.Address.Labels[0][0]")
	assert.Equal(t, fe.Namespace(), "namedDeepUser.地址.标签[0][0]")
	assert.Equal(t, segmentStrings(fe.PathSegments()), []string{
		"struct:namedDeepUser/namedDeepUser",
		"struct:Address/地址",
		"struct:Labels/标签",
		"index:0/0",
		"index:0/0",
	})

	fe2 := findNamedError(t, errs, "namedDeepUser.Address.Labels[1][1]")
	assert.Equal(t, fe2.Namespace(), "namedDeepUser.地址.标签[1][1]")
}

// TestStructNamedWithGlobalTagNameFunc verifies the per-call callback wins for
// the call, while a global TagNameFunc keeps driving normal Struct calls.
func TestStructNamedWithGlobalTagNameFunc(t *testing.T) {
	v := New()
	v.RegisterTagNameFunc(func(fld reflect.StructField) string {
		return "global-" + fld.Name
	})

	user := newNamedUserFixture()

	globalErrs := v.Struct(user).(ValidationErrors)
	fe := findNamedError(t, globalErrs, "namedUser.UserName")
	assert.Equal(t, fe.Namespace(), "namedUser.global-UserName")

	jsonErrs := v.StructNamed(user, jsonFieldName).(ValidationErrors)
	fe = findNamedError(t, jsonErrs, "namedUser.UserName")
	assert.Equal(t, fe.Namespace(), "namedUser.user_name")

	cnErrs := v.StructNamed(user, chineseFieldName).(ValidationErrors)
	fe = findNamedError(t, cnErrs, "namedUser.UserName")
	assert.Equal(t, fe.Namespace(), "namedUser.用户名")

	// global function must still be registered and unaffected
	assert.Equal(t, v.hasTagNameFunc, true)
	globalAgain := v.Struct(user).(ValidationErrors)
	fe = findNamedError(t, globalAgain, "namedUser.UserName")
	assert.Equal(t, fe.Namespace(), "namedUser.global-UserName")
}
