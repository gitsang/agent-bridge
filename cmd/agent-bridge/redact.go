package main

import (
	"reflect"
	"strings"
)

const redactedValue = "***"

func redactLogValue(value any) any {
	if value == nil {
		return nil
	}
	return redactValue(reflect.ValueOf(value)).Interface()
}

func redactValue(value reflect.Value) reflect.Value {
	if !value.IsValid() {
		return value
	}
	if value.Kind() == reflect.Pointer || value.Kind() == reflect.Interface {
		if value.IsNil() {
			return value
		}
		return redactValue(value.Elem())
	}

	switch value.Kind() {
	case reflect.Map:
		redacted := reflect.MakeMapWithSize(value.Type(), value.Len())
		for _, key := range value.MapKeys() {
			mapValue := value.MapIndex(key)
			if isSensitiveName(fmtValue(key)) {
				redacted.SetMapIndex(key, reflect.ValueOf(redactedValue))
				continue
			}
			redacted.SetMapIndex(key, redactValue(mapValue))
		}
		return redacted
	case reflect.Slice:
		redacted := reflect.MakeSlice(value.Type(), value.Len(), value.Len())
		for index := 0; index < value.Len(); index++ {
			redacted.Index(index).Set(redactValue(value.Index(index)))
		}
		return redacted
	case reflect.Array:
		redacted := reflect.New(value.Type()).Elem()
		for index := 0; index < value.Len(); index++ {
			redacted.Index(index).Set(redactValue(value.Index(index)))
		}
		return redacted
	case reflect.Struct:
		redacted := reflect.New(value.Type()).Elem()
		for index := 0; index < value.NumField(); index++ {
			field := value.Type().Field(index)
			if !redacted.Field(index).CanSet() {
				continue
			}
			if isSensitiveName(field.Name) || isSensitiveName(field.Tag.Get("json")) || isSensitiveName(field.Tag.Get("yaml")) {
				redacted.Field(index).Set(reflect.ValueOf(redactedValue))
				continue
			}
			redacted.Field(index).Set(redactValue(value.Field(index)))
		}
		return redacted
	default:
		return value
	}
}

func fmtValue(value reflect.Value) string {
	if !value.IsValid() {
		return ""
	}
	return strings.TrimSpace(value.String())
}

func isSensitiveName(name string) bool {
	name = strings.ToLower(strings.TrimSpace(strings.Split(name, ",")[0]))
	return strings.Contains(name, "token") || strings.Contains(name, "password") || strings.Contains(name, "secret") || strings.Contains(name, "api_key") || strings.Contains(name, "apikey") || strings.Contains(name, "credential")
}
