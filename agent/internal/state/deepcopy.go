package state

import (
	"reflect"
	"time"
)

var timeType = reflect.TypeOf(time.Time{})

// deepCopy returns a copy of v that shares no pointer, map or slice with it. It walks the value with
// reflection, so a field added to State later is copied correctly without touching this file. Every
// struct reachable from State has only exported fields (a test guards it).
func deepCopy[T any](v T) T {
	out := reflect.New(reflect.TypeOf(v)).Elem()
	copyValue(out, reflect.ValueOf(v))
	return out.Interface().(T)
}

func copyValue(dst, src reflect.Value) {
	switch src.Kind() {
	case reflect.Pointer:
		if src.IsNil() {
			return
		}
		dst.Set(reflect.New(src.Type().Elem()))
		copyValue(dst.Elem(), src.Elem())
	case reflect.Interface:
		if src.IsNil() {
			return
		}
		inner := reflect.New(src.Elem().Type()).Elem()
		copyValue(inner, src.Elem())
		dst.Set(inner)
	case reflect.Map:
		if src.IsNil() {
			return
		}
		dst.Set(reflect.MakeMapWithSize(src.Type(), src.Len()))
		iter := src.MapRange()
		for iter.Next() {
			k := reflect.New(src.Type().Key()).Elem()
			copyValue(k, iter.Key())
			v := reflect.New(src.Type().Elem()).Elem()
			copyValue(v, iter.Value())
			dst.SetMapIndex(k, v)
		}
	case reflect.Slice:
		if src.IsNil() {
			return
		}
		dst.Set(reflect.MakeSlice(src.Type(), src.Len(), src.Len()))
		if src.Type().Elem().Kind() == reflect.Uint8 {
			reflect.Copy(dst, src) // json.RawMessage and other byte slices
			return
		}
		for i := 0; i < src.Len(); i++ {
			copyValue(dst.Index(i), src.Index(i))
		}
	case reflect.Struct:
		if src.Type() == timeType {
			dst.Set(src) // time.Time has unexported fields and is immutable
			return
		}
		for i := 0; i < src.NumField(); i++ {
			copyValue(dst.Field(i), src.Field(i))
		}
	default:
		dst.Set(src)
	}
}
