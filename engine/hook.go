package engine

import (
	"regexp"
	"strings"
)

type HookPos int

const (
	PosAhead  HookPos = 1
	PosBehind HookPos = 2
)

type IMatcher interface {
	Match(method, path string) bool
}
type HookOpts func(m IMatcher)
type Hook struct {
	group       *RouterGroup
	matcher     IMatcher
	Pos         HookPos
	HandlerFunc HandlerFunc
}
type Contain struct {
	sub string
}

func Contains(sub string, opts ...HookOpts) *Contain {

	return &Contain{sub: sub}

}
func (c *Contain) Match(method, path string) bool {
	return strings.Contains(path, c.sub)
}

type Reg struct {
	regexp *regexp.Regexp
}

func RegExp(expr string) *Reg {
	reg, err := regexp.Compile(expr)
	if err != nil {
		panic(err.Error())
	}
	return &Reg{regexp: reg}
}

func (r *Reg) Match(method, path string) bool {
	return r.regexp.MatchString(path)

}

type Identically struct {
	path string
}

func Identical(path string) *Identically {
	return &Identically{path: path}
}

func (i *Identically) Match(method, path string) bool {
	return path == i.path
}

type Pre struct {
	prefix    string
	exclusive []string
}

func Prefix(prefix string, exclusive []string) *Pre {

	return &Pre{prefix: prefix, exclusive: exclusive}
}
func (p *Pre) Match(method, path string) bool {
	for _, s := range p.exclusive {
		if s == path {
			return false
		}
	}
	return strings.HasPrefix(path, p.prefix)
}

func (e *Engine) Use(pos HookPos, matcher IMatcher, fn HandlerFunc) {
	e.addHook(nil, pos, matcher, fn)
}

func (e *Engine) addHook(group *RouterGroup, pos HookPos, matcher IMatcher, fn HandlerFunc) {
	e.checkMutable()
	if pos != PosAhead && pos != PosBehind {
		panic("holy: invalid hook position")
	}
	if matcher == nil {
		panic("holy: nil matcher")
	}
	validateHandlers([]HandlerFunc{fn})
	e.hooks = append(e.hooks, Hook{
		group:       group,
		matcher:     matcher,
		Pos:         pos,
		HandlerFunc: fn,
	})
}
