package contract

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
)

// DescribedField is one field of a wire contract message, named by its full
// dotted path, with the description its schema gives it.
type DescribedField struct {
	Path        string
	Description string
}

// EnrollmentRequestFields lists every field an enrollment request carries, at
// any depth, in the order EnrollmentRequest declares them, each with the
// description from the enrollment schema. It panics when a field of the Go
// type has no description in the schema, so the two cannot drift silently.
//
// @spec: field-allowlist#statement-covers-enrollment
func EnrollmentRequestFields() []DescribedField {
	b, err := schemaFS.ReadFile(EnrollmentSchema)
	if err != nil {
		panic(err)
	}
	type node struct {
		Description string          `json:"description"`
		Properties  map[string]node `json:"properties"`
	}
	var doc struct {
		Defs map[string]node `json:"$defs"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		panic(err)
	}
	var out []DescribedField
	var walk func(prefix string, t reflect.Type, props map[string]node)
	walk = func(prefix string, t reflect.Type, props map[string]node) {
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			name := strings.Split(f.Tag.Get("json"), ",")[0]
			n, ok := props[name]
			if !ok || n.Description == "" {
				panic(fmt.Sprintf("enrollment request field %s%s has no description in %s", prefix, name, EnrollmentSchema))
			}
			out = append(out, DescribedField{Path: prefix + name, Description: n.Description})
			ft := f.Type
			if ft.Kind() == reflect.Pointer {
				ft = ft.Elem()
			}
			if ft.Kind() == reflect.Struct {
				walk(prefix+name+".", ft, n.Properties)
			}
		}
	}
	walk("", reflect.TypeOf(EnrollmentRequest{}), doc.Defs["request"].Properties)
	return out
}
