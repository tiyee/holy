package engine

import (
	"iter"
	"strings"
)

// radixRouter 按 HTTP method 维护独立压缩前缀树，是 IRouter 的默认实现。
type radixRouter struct {
	Fallbacks
	trees map[string]*node
}

func newRadixRouter() *radixRouter {
	return &radixRouter{trees: make(map[string]*node)}
}

func (r *radixRouter) Insert(method, path string, route *Route) {
	root := r.trees[method]
	if root == nil {
		root = &node{}
		r.trees[method] = root
	}
	root.insert(path, route)
}

func (r *radixRouter) Lookup(method, path string, params *Params) *Route {
	root := r.trees[method]
	if root == nil {
		return nil
	}
	return root.lookup(path, path, params)
}

func (r *radixRouter) Methods() []string {
	methods := make([]string, 0, len(r.trees))
	for method := range r.trees {
		methods = append(methods, method)
	}
	return methods
}

func (r *radixRouter) Iter() iter.Seq[*Route] {
	return func(yield func(*Route) bool) {
		for _, root := range r.trees {
			if !root.iterate(yield) {
				return
			}
		}
	}
}

// iterate 返回 false 表示消费方已提前退出，须立即停止整棵遍历。
func (n *node) iterate(yield func(*Route) bool) bool {
	if n.route != nil && !yield(n.route) {
		return false
	}
	for _, child := range n.children {
		if !child.iterate(yield) {
			return false
		}
	}
	if n.param != nil && !n.param.iterate(yield) {
		return false
	}
	if n.catchAll != nil && !n.catchAll.iterate(yield) {
		return false
	}
	return true
}

// node 压缩连续静态字节；同一节点的静态子边首字节各不相同。
// 参数与静态边分开存储，使静态匹配失败后可以回退到参数分支。
type node struct {
	path            string
	indices         string
	children        []*node
	param, catchAll *node
	name            string
	route           *Route
}

func (n *node) insert(path string, route *Route) {
	for len(path) > 0 {
		i := strings.IndexAny(path, ":*")
		if i < 0 {
			n = n.staticChild(path)
			break
		}
		if i > 0 {
			n = n.staticChild(path[:i])
			path = path[i:]
		}
		end := strings.IndexByte(path, '/')
		if end < 0 {
			end = len(path)
		}
		name := path[1:end]
		child := &n.param
		if path[0] == '*' {
			child = &n.catchAll
		}
		if n.param != nil && path[0] == '*' || n.catchAll != nil && path[0] == ':' {
			panic("holy: conflicting wildcard in " + route.Path)
		}
		if *child == nil {
			*child = &node{name: name}
		} else if (*child).name != name {
			panic("holy: conflicting parameter names in " + route.Path)
		}
		n, path = *child, path[end:]
	}
	if n.route != nil {
		panic("holy: duplicate route " + route.Method + " " + route.Path)
	}
	n.route = route
}

func (n *node) staticChild(path string) *node {
	for {
		index := strings.IndexByte(n.indices, path[0])
		if index < 0 {
			child := &node{path: path}
			n.indices += path[:1]
			n.children = append(n.children, child)
			return child
		}
		child := n.children[index]
		i := 0
		for i < min(len(path), len(child.path)) && path[i] == child.path[i] {
			i++
		}
		if i < len(child.path) {
			suffix := *child
			suffix.path = child.path[i:]
			*child = node{path: child.path[:i], indices: suffix.path[:1], children: []*node{&suffix}}
		}
		if i == len(path) {
			return child
		}
		n, path = child, path[i:]
	}
}

// lookup 优先静态，其次单段参数，最后 catch-all；失败分支须撤销参数。
// 保留 fullPath，catch-all 可连同前一个 / 一起截取；params 为 nil 时只探测匹配。
func (n *node) lookup(path, fullPath string, params *Params) *Route {
	if path == "" && n.route != nil {
		return n.route
	}
	if path != "" {
		if i := strings.IndexByte(n.indices, path[0]); i >= 0 {
			child := n.children[i]
			if strings.HasPrefix(path, child.path) {
				if route := child.lookup(path[len(child.path):], fullPath, params); route != nil {
					return route
				}
			}
		}
		if n.param != nil && path[0] != '/' {
			end := strings.IndexByte(path, '/')
			if end < 0 {
				end = len(path)
			}
			size := 0
			if params != nil {
				size = len(*params)
				*params = append(*params, Param{Key: n.param.name, Value: path[:end]})
			}
			if route := n.param.lookup(path[end:], fullPath, params); route != nil {
				return route
			}
			if params != nil {
				*params = (*params)[:size]
			}
		}
	}
	if n.catchAll != nil {
		if params != nil {
			*params = append(*params, Param{Key: n.catchAll.name, Value: fullPath[len(fullPath)-len(path)-1:]})
		}
		return n.catchAll.route
	}
	return nil
}

var _ IRouter = (*radixRouter)(nil)
