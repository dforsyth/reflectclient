package reflectclient

import (
	"fmt"
	"reflect"
)

func in(needle string, haystack []string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

func extractFieldValue(value reflect.Value, name string) string {
	return fmt.Sprint(value.FieldByName(name).Interface())
}

func elementType(in reflect.Type) reflect.Type {
	// TODO: At some point this should support other types I guess...
	if in.Kind() == reflect.Ptr {
		return in.Elem()
	}
	return in
}

func elementValue(in reflect.Value) reflect.Value {
	if in.Kind() == reflect.Ptr || in.Kind() == reflect.Interface {
		return in.Elem()
	}
	return in
}
