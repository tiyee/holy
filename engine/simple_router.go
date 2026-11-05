package engine

import (
	"iter"
	"slices"
	"strings"
)

// SimpleRouter 将路由按 method、path 排序存入一个 slice，通过二分进行精确匹配。
// 不解析路径参数、通配符或正则表达式，也不修改 Lookup 的参数切片。
// 零值可用；注册期间不支持并发，注册完成后可并发查找和遍历。
type SimpleRouter struct {
	Fallbacks
	routes []simpleRoute
}

type simpleRoute struct {
	method, path string
	route        *Route
}

func NewSimpleRouter() *SimpleRouter { return &SimpleRouter{} }

func (r *SimpleRouter) Insert(method, path string, route *Route) {
	if !validMethod(method) {
		panic("holy: invalid HTTP method " + method)
	}
	validatePath(path)
	if route == nil {
		panic("holy: nil route")
	}
	entry := simpleRoute{method: method, path: path, route: route}
	i, found := slices.BinarySearchFunc(r.routes, entry, compareSimpleRoute)
	if found {
		panic("holy: duplicate route " + method + " " + path)
	}
	r.routes = slices.Insert(r.routes, i, entry)
}

func compareSimpleRoute(a, b simpleRoute) int {
	if order := strings.Compare(a.method, b.method); order != 0 {
		return order
	}
	return strings.Compare(a.path, b.path)
}

func (r *SimpleRouter) Lookup(method, path string, _ *Params) *Route {
	i, found := slices.BinarySearchFunc(r.routes, simpleRoute{method: method, path: path}, compareSimpleRoute)
	if !found {
		return nil
	}
	return r.routes[i].route
}

func (r *SimpleRouter) Methods() []string {
	var methods []string
	for _, entry := range r.routes {
		if len(methods) == 0 || methods[len(methods)-1] != entry.method {
			methods = append(methods, entry.method)
		}
	}
	return methods
}

func (r *SimpleRouter) Iter() iter.Seq[*Route] {
	return func(yield func(*Route) bool) {
		for _, entry := range r.routes {
			if !yield(entry.route) {
				return
			}
		}
	}
}

var _ IRouter = (*SimpleRouter)(nil)
