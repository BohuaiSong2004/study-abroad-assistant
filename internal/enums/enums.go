// Package enums converts protobuf enums to and from the lower_snake names
// used by the HTTP API and XML exports ("DEGREE_MASTER" <-> "master").
package enums

import (
	"fmt"
	"strings"

	"google.golang.org/protobuf/reflect/protoreflect"
)

// Name returns the lower-cased value name without the type prefix, or ""
// for the zero (UNSPECIFIED) value.
func Name(e protoreflect.Enum, prefix string) string {
	v := e.Descriptor().Values().ByNumber(e.Number())
	if v == nil {
		return ""
	}
	n := strings.TrimPrefix(string(v.Name()), prefix)
	if n == "UNSPECIFIED" {
		return ""
	}
	return strings.ToLower(n)
}

// Parse maps a name back to its number using the generated name->value map.
// "" maps to 0.
func Parse(s, prefix string, values map[string]int32) (int32, error) {
	if s == "" {
		return 0, nil
	}
	v, ok := values[prefix+strings.ToUpper(s)]
	if !ok || v == 0 {
		return 0, fmt.Errorf("unknown value %q", s)
	}
	return v, nil
}
