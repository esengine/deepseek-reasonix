package transcript

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"
)

// mapContentStrings copies metadata while sharing immutable strings. Large
// bodies are replaced before JSON encoding, so page/content reads never
// serialize an entire tool payload just to return one small chunk.
func mapContentStrings(value reflect.Value, path []string, visit func(string, []string) string) reflect.Value {
	switch value.Kind() {
	case reflect.String:
		out := reflect.New(value.Type()).Elem()
		out.SetString(visit(value.String(), path))
		return out
	case reflect.Pointer, reflect.Interface:
		if value.IsNil() {
			return value
		}
		child := mapContentStrings(value.Elem(), path, visit)
		out := reflect.New(value.Type()).Elem()
		if value.Kind() == reflect.Pointer {
			pointer := reflect.New(value.Type().Elem())
			pointer.Elem().Set(child)
			out.Set(pointer)
		} else {
			out.Set(child)
		}
		return out
	case reflect.Struct:
		out := reflect.New(value.Type()).Elem()
		out.Set(value)
		for i := range value.NumField() {
			field := value.Type().Field(i)
			name := strings.Split(field.Tag.Get("json"), ",")[0]
			if field.PkgPath != "" || name == "-" {
				continue
			}
			if name == "" {
				name = field.Name
			}
			out.Field(i).Set(mapContentStrings(value.Field(i), appendPath(path, name), visit))
		}
		return out
	case reflect.Slice, reflect.Array:
		if value.Kind() == reflect.Slice && value.IsNil() {
			return value
		}
		var out reflect.Value
		if value.Kind() == reflect.Slice {
			out = reflect.MakeSlice(value.Type(), value.Len(), value.Len())
		} else {
			out = reflect.New(value.Type()).Elem()
		}
		for i := range value.Len() {
			out.Index(i).Set(mapContentStrings(value.Index(i), appendPath(path, strconv.Itoa(i)), visit))
		}
		return out
	case reflect.Map:
		if value.IsNil() {
			return value
		}
		out := reflect.MakeMapWithSize(value.Type(), value.Len())
		for iterator := value.MapRange(); iterator.Next(); {
			out.SetMapIndex(iterator.Key(), mapContentStrings(iterator.Value(), appendPath(path, fmt.Sprint(iterator.Key().Interface())), visit))
		}
		return out
	default:
		return value
	}
}

func appendPath(path []string, key string) []string {
	return append(append([]string(nil), path...), key)
}

func contentStringAt(message Message, path []string) (string, bool) {
	value := reflect.ValueOf(message)
	for _, part := range path {
		for value.IsValid() && (value.Kind() == reflect.Pointer || value.Kind() == reflect.Interface) {
			if value.IsNil() {
				return "", false
			}
			value = value.Elem()
		}
		if !value.IsValid() {
			return "", false
		}
		switch value.Kind() {
		case reflect.Struct:
			var child reflect.Value
			for i := range value.NumField() {
				field := value.Type().Field(i)
				name := strings.Split(field.Tag.Get("json"), ",")[0]
				if name == "" {
					name = field.Name
				}
				if field.PkgPath == "" && name != "-" && name == part {
					child = value.Field(i)
					break
				}
			}
			value = child
		case reflect.Slice, reflect.Array:
			index, err := strconv.Atoi(part)
			if err != nil || index < 0 || index >= value.Len() {
				return "", false
			}
			value = value.Index(index)
		case reflect.Map:
			if value.Type().Key().Kind() != reflect.String {
				return "", false
			}
			key := reflect.New(value.Type().Key()).Elem()
			key.SetString(part)
			value = value.MapIndex(key)
		default:
			return "", false
		}
	}
	if !value.IsValid() || value.Kind() != reflect.String {
		return "", false
	}
	return value.String(), true
}

func retainedBytes(value reflect.Value) int {
	if !value.IsValid() {
		return 0
	}
	size := int(value.Type().Size())
	switch value.Kind() {
	case reflect.String:
		return size + value.Len()
	case reflect.Pointer, reflect.Interface:
		if !value.IsNil() {
			size += retainedBytes(value.Elem())
		}
	case reflect.Struct:
		for i := range value.NumField() {
			size += retainedBytes(value.Field(i))
		}
	case reflect.Slice, reflect.Array:
		for i := range value.Len() {
			size += retainedBytes(value.Index(i))
		}
	case reflect.Map:
		for iterator := value.MapRange(); iterator.Next(); {
			size += 64 + retainedBytes(iterator.Key()) + retainedBytes(iterator.Value())
		}
	}
	return size
}
