package main

import (
	"fmt"
	"strings"

	"github.com/go-playground/validator/v10"
)

// User demonstrates validating one struct with two different per-call field
// naming rules without registering a global RegisterTagNameFunc.
type User struct {
	FirstName string   `json:"first_name" cn:"名" validate:"required"`
	LastName  string   `json:"last_name" cn:"姓" validate:"required"`
	Emails    []string `json:"emails" cn:"邮箱" validate:"dive,required,email"`
}

func jsonName(info validator.FieldInfo) string {
	name := strings.SplitN(info.Field.Tag.Get("json"), ",", 2)[0]
	if name == "-" {
		return ""
	}
	return name
}

func chineseName(info validator.FieldInfo) string {
	return info.Field.Tag.Get("cn")
}

func main() {
	validate := validator.New()

	user := User{
		Emails: []string{"not-an-email"},
	}

	for label, fn := range map[string]validator.FieldNameFunc{
		"json": jsonName,
		"cn":   chineseName,
	} {
		err := validate.StructNamed(user, fn)
		fmt.Println("==", label)
		for _, fe := range err.(validator.ValidationErrors) {
			// StructNamespace is always the original Go field path, even when
			// display names contain '.', '[' or ']' and cannot be split safely.
			fmt.Printf("go path:      %s\n", fe.StructNamespace())
			fmt.Printf("display path: %s\n", fe.DisplayNamespace())
			for _, seg := range fe.PathSegments() {
				fmt.Printf("  %-6s %-10s -> %s\n", seg.Kind, seg.Name, seg.DisplayName)
			}
		}
	}
}
