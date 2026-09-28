package validator

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/go-playground/assert/v2"
)

type namedAddress struct {
	Street string `json:"street" cn:"街道" validate:"required"`
}

type namedEmbedded struct {
	namedAddress
}

type namedItem struct {
	Name string `json:"name" cn:"名称" validate:"required"`
}

type namedUser struct {
	namedEmbedded
	UserName string            `json:"user_name" cn:"用户名" validate:"required"`
	Items    []namedItem       `json:"items" cn:"条目" validate:"dive"`
	Meta     map[string]string `json:"meta" cn:"元数据" validate:"dive,keys,required,endkeys,required"`
	Weird    string            `json:"we.ir[d]" cn:"奇.怪[x]" validate:"required"`
	Ignored  string            `json:"ignored" cn:"忽略" validate:"-"`
}

func jsonFieldName(info FieldInfo) string {
	name := strings.SplitN(info.Field.Tag.Get("json"), ",", 2)[0]
	if name == "-" {
		return ""
	}
	return name
}

func chineseFieldName(info FieldInfo) string {
	return info.Field.Tag.Get("cn")
}

func findNamedError(t *testing.T, errs ValidationErrors, structNs string) FieldError {
	t.Helper()
	for _, fe := range errs {
		if fe.StructNamespace() == structNs {
			return fe
		}
	}
	t.Fatalf("error with StructNamespace %q not found in %v", structNs, errs)
	return nil
}

func segmentStrings(segs []FieldPathSegment) []string {
	out := make([]string, 0, len(segs))
	for _, seg := range segs {
		out = append(out, seg.Kind.String()+":"+seg.Name+"/"+seg.DisplayName)
	}
	return out
}

func newNamedUserFixture() namedUser {
	return namedUser{
		namedEmbedded: namedEmbedded{
			namedAddress: namedAddress{},
		},
		Items: []namedItem{{Name: ""}},
		Meta:  map[string]string{"a.b": ""},
	}
}

// TestStructNamedSameTypeDifferentNames verifies that validating the very same
// type in two calls with different FieldNameFunc values keeps the display
// names scoped to each call while the original Go field path is identical.
func TestStructNamedSameTypeDifferentNames(t *testing.T) {
	v := New()
	user := newNamedUserFixture()

	jsonErrs := v.StructNamed(user, jsonFieldName).(ValidationErrors)
	cnErrs := v.StructNamed(user, chineseFieldName).(ValidationErrors)

	assert.Equal(t, len(jsonErrs), len(cnErrs))

	cases := []struct {
		structNs string
		jsonNs   string
		cnNs     string
		jsonFld  string
		cnFld    string
	}{
		{"namedUser.namedEmbedded.namedAddress.Street", "namedUser.namedEmbedded.namedAddress.street", "namedUser.namedEmbedded.namedAddress.街道", "street", "街道"},
		{"namedUser.UserName", "namedUser.user_name", "namedUser.用户名", "user_name", "用户名"},
		{"namedUser.Items[0].Name", "namedUser.items[0].name", "namedUser.条目[0].名称", "name", "名称"},
		{"namedUser.Meta[a.b]", "namedUser.meta[a.b]", "namedUser.元数据[a.b]", "meta[a.b]", "元数据[a.b]"},
		{"namedUser.Weird", "namedUser.we.ir[d]", "namedUser.奇.怪[x]", "we.ir[d]", "奇.怪[x]"},
	}

	for _, tc := range cases {
		jsonFE := findNamedError(t, jsonErrs, tc.structNs)
		cnFE := findNamedError(t, cnErrs, tc.structNs)
		structLeaf := tc.structNs
		if bracket := strings.Index(structLeaf, "["); bracket != -1 {
			if closeBracket := strings.Index(structLeaf, "]"); closeBracket != -1 && closeBracket+1 < len(structLeaf) {
				structLeaf = structLeaf[closeBracket+2:]
			} else {
				structLeaf = tc.structNs[strings.LastIndex(tc.structNs[:bracket], ".")+1:]
			}
		} else {
			structLeaf = tc.structNs[strings.LastIndex(tc.structNs, ".")+1:]
		}

		assert.Equal(t, jsonFE.Namespace(), tc.jsonNs)
		assert.Equal(t, cnFE.Namespace(), tc.cnNs)
		assert.Equal(t, jsonFE.DisplayNamespace(), tc.jsonNs)
		assert.Equal(t, cnFE.DisplayNamespace(), tc.cnNs)
		assert.Equal(t, jsonFE.Field(), tc.jsonFld)
		assert.Equal(t, cnFE.Field(), tc.cnFld)
		assert.Equal(t, jsonFE.StructField(), structLeaf)
		assert.Equal(t, cnFE.StructField(), structLeaf)
	}

	// validating the same type again without a name func must not see cached names
	plainErrs := v.Struct(user).(ValidationErrors)
	fe := findNamedError(t, plainErrs, "namedUser.UserName")
	assert.Equal(t, fe.Namespace(), "namedUser.UserName")
	assert.Equal(t, len(fe.PathSegments()), 0)
}

// TestStructNamedAnonymousEmbedded verifies that anonymous embedded struct
// fields are composed using every struct field that was actually walked.
func TestStructNamedAnonymousEmbedded(t *testing.T) {
	v := New()
	user := newNamedUserFixture()

	errs := v.StructNamed(user, jsonFieldName).(ValidationErrors)
	fe := findNamedError(t, errs, "namedUser.namedEmbedded.namedAddress.Street")

	assert.Equal(t, fe.Namespace(), "namedUser.namedEmbedded.namedAddress.street")
	assert.Equal(t, segmentStrings(fe.PathSegments()), []string{
		"struct:namedUser/namedUser",
		"struct:namedEmbedded/namedEmbedded",
		"struct:namedAddress/namedAddress",
		"struct:Street/street",
	})
}

// TestStructNamedDive verifies slice indexes and map keys survive renaming and
// are reported as distinct segment kinds.
func TestStructNamedDive(t *testing.T) {
	v := New()
	user := newNamedUserFixture()

	errs := v.StructNamed(user, chineseFieldName).(ValidationErrors)

	item := findNamedError(t, errs, "namedUser.Items[0].Name")
	assert.Equal(t, item.Namespace(), "namedUser.条目[0].名称")
	assert.Equal(t, segmentStrings(item.PathSegments()), []string{
		"struct:namedUser/namedUser",
		"struct:Items/条目",
		"index:0/0",
		"struct:Name/名称",
	})

	key := findNamedError(t, errs, "namedUser.Meta[a.b]")
	assert.Equal(t, key.Namespace(), "namedUser.元数据[a.b]")
	assert.Equal(t, segmentStrings(key.PathSegments()), []string{
		"struct:namedUser/namedUser",
		"struct:Meta/元数据",
		"key:a.b/a.b",
	})
}

// TestStructNamedIgnoredField verifies fields skipped via validate:"-" are
// never validated and never reach the naming callback.
func TestStructNamedIgnoredField(t *testing.T) {
	v := New()
	user := newNamedUserFixture()
	user.Ignored = ""

	var seen []string
	errs := v.StructNamed(user, func(info FieldInfo) string {
		seen = append(seen, info.Field.Name)
		return chineseFieldName(info)
	}).(ValidationErrors)

	for _, fe := range errs {
		assert.NotEqual(t, fe.StructNamespace(), "namedUser.Ignored")
	}
	for _, name := range seen {
		assert.NotEqual(t, name, "Ignored")
	}
}

type namedCustom struct {
	Value string `json:"value" cn:"数值"`
}

// TestStructNamedStructLevel verifies errors reported from a custom struct
// level validation keep both paths and segments too.
func TestStructNamedStructLevel(t *testing.T) {
	v := New()
	v.RegisterStructValidation(func(sl StructLevel) {
		current := sl.Current().Interface().(namedCustom)
		if current.Value != "ok" {
			sl.ReportError(current.Value, "value", "Value", "must_be_ok", "ok")
		}
	}, namedCustom{})

	errs := v.StructNamed(namedCustom{}, jsonFieldName).(ValidationErrors)
	assert.Equal(t, len(errs), 1)
	fe := errs[0]
	assert.Equal(t, fe.StructNamespace(), "namedCustom.Value")
	assert.Equal(t, fe.Namespace(), "namedCustom.value")
	assert.Equal(t, fe.Tag(), "must_be_ok")
	assert.Equal(t, segmentStrings(fe.PathSegments()), []string{
		"struct:namedCustom/namedCustom",
		"struct:Value/value",
	})

	cnErrs := v.StructNamed(namedCustom{}, chineseFieldName).(ValidationErrors)
	assert.Equal(t, cnErrs[0].Namespace(), "namedCustom.数值")
}

// TestStructNamedGlobalTagFuncUnaffected verifies the per-call name func does
// not require or register a global tag name function.
func TestStructNamedGlobalTagFuncUnaffected(t *testing.T) {
	v := New()
	assert.Equal(t, v.hasTagNameFunc, false)

	_ = v.StructNamed(newNamedUserFixture(), jsonFieldName)
	_ = v.StructNamed(newNamedUserFixture(), chineseFieldName)

	assert.Equal(t, v.hasTagNameFunc, false)
	assert.Equal(t, v.tagNameFunc, (TagNameFunc)(nil))
}

// TestStructNamedContext ensures the context aware entry forwards the context.
func TestStructNamedContext(t *testing.T) {
	v := New()
	type ctxKey string
	ctx := context.WithValue(context.Background(), ctxKey("k"), "v")

	v.RegisterStructValidationCtx(func(ctx context.Context, sl StructLevel) {
		assert.Equal(t, ctx.Value(ctxKey("k")), "v")
	}, namedCustom{})

	err := v.StructNamedCtx(ctx, namedCustom{}, jsonFieldName)
	_, ok := err.(*InvalidValidationError)
	assert.Equal(t, ok, false)
}

// TestStructNamedInvalidInput keeps Struct-compatible input handling.
func TestStructNamedInvalidInput(t *testing.T) {
	v := New()
	err := v.StructNamed(1, jsonFieldName)
	_, ok := err.(*InvalidValidationError)
	assert.Equal(t, ok, true)
}

// TestStructNamedConcurrent runs many calls with different naming functions
// in parallel; run under -race to prove there is no cross-call cache/state
// pollution.
func TestStructNamedConcurrent(t *testing.T) {
	v := New()

	const goroutines = 16
	const iterations = 100

	funcs := map[string]FieldNameFunc{
		"json": jsonFieldName,
		"cn":   chineseFieldName,
		"go": func(info FieldInfo) string {
			return ""
		},
		"dynamic": func(info FieldInfo) string {
			return info.Field.Name + "x"
		},
	}

	var wg sync.WaitGroup
	wg.Add(goroutines)

	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				user := newNamedUserFixture()
				for name, fn := range funcs {
					errs := v.StructNamed(user, fn).(ValidationErrors)
					var fe FieldError
					for _, candidate := range errs {
						if candidate.StructNamespace() == "namedUser.UserName" {
							fe = candidate
						}
					}
					if fe == nil {
						panic("namedUser.UserName error missing")
					}
					switch name {
					case "json":
						if fe.Namespace() != "namedUser.user_name" {
							panic(fmt.Sprintf("json pollution: %s", fe.Namespace()))
						}
					case "cn":
						if fe.Namespace() != "namedUser.用户名" {
							panic(fmt.Sprintf("cn pollution: %s", fe.Namespace()))
						}
					case "go":
						if fe.Namespace() != "namedUser.UserName" {
							panic(fmt.Sprintf("go pollution: %s", fe.Namespace()))
						}
					case "dynamic":
						if fe.Namespace() != "namedUser.UserNamex" {
							panic(fmt.Sprintf("dynamic pollution: %s", fe.Namespace()))
						}
					}
				}
			}
		}()
	}

	wg.Wait()
}

// TestStructNamedStructNamespaceIdentifiesError documents that StructNamespace
// plus Tag uniquely locates the same failing field regardless of naming func.
func TestStructNamedStructNamespaceIdentifiesError(t *testing.T) {
	v := New()
	user := newNamedUserFixture()

	jsonErrs := v.StructNamed(user, jsonFieldName).(ValidationErrors)
	cnErrs := v.StructNamed(user, chineseFieldName).(ValidationErrors)
	plainErrs := v.Struct(user).(ValidationErrors)

	key := func(errs ValidationErrors) map[string]string {
		out := make(map[string]string, len(errs))
		for _, fe := range errs {
			out[fe.StructNamespace()+"#"+fe.Tag()] = fe.Namespace()
		}
		return out
	}

	jsonMap, cnMap, plainMap := key(jsonErrs), key(cnErrs), key(plainErrs)
	assert.Equal(t, len(jsonMap), len(cnMap))
	assert.Equal(t, len(jsonMap), len(plainMap))
	for id := range jsonMap {
		_, cnOK := cnMap[id]
		_, plainOK := plainMap[id]
		assert.Equal(t, cnOK, true)
		assert.Equal(t, plainOK, true)
	}
}

// ExampleValidate_structNamed shows how to validate the same struct with a
// per-call field naming rule and how both paths are reported.
func ExampleValidate_structNamed() {
	type profile struct {
		NickName string `json:"nick_name" cn:"昵称" validate:"required"`
	}

	validate := New()

	err := validate.StructNamed(profile{}, func(info FieldInfo) string {
		return info.Field.Tag.Get("cn")
	})
	for _, fe := range err.(ValidationErrors) {
		fmt.Println("go:", fe.StructNamespace())
		fmt.Println("display:", fe.DisplayNamespace())
		for _, seg := range fe.PathSegments() {
			fmt.Printf("segment %s: %s -> %s\n", seg.Kind, seg.Name, seg.DisplayName)
		}
	}
	// Output:
	// go: profile.NickName
	// display: profile.昵称
	// segment struct: profile -> profile
	// segment struct: NickName -> 昵称
}
