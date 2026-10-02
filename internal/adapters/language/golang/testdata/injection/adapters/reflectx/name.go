package reflectx

import "reflect"

func TypeName(v any) string {
	return reflect.TypeOf(v).String()
}
