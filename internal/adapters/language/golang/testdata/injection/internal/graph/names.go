package graph

import "github.com/smarty/injection/adapters/reflectx"

func typeName(v any) string {
	return reflectx.TypeName(v)
}
