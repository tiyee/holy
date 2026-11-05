package engine

import (
	"fmt"
	"reflect"
	"testing"
)

func TestRadixCatchAllOriginalPath(t *testing.T) {
	testRouterCatchAllOriginalPath(t, func() IRouter { return newRadixRouter() })
}

func testRouterCatchAllOriginalPath(t *testing.T, newRouter func() IRouter) {
	for _, reverse := range []bool{false, true} {
		t.Run(fmt.Sprint(reverse), func(t *testing.T) {
			r := newRouter()
			paths := []string{"/*root", "/orgs/:org/files/*rest", "/orgs/:org/files/static/:id/end"}
			if reverse {
				paths[0], paths[2] = paths[2], paths[0]
			}
			for _, path := range paths {
				r.Insert("GET", path, &Route{Method: "GET", Path: path})
			}
			for _, tc := range []struct {
				path, route string
				params      Params
			}{
				{"/", "/*root", Params{{"root", "/"}}},
				{"//a/../中文/%2F", "/*root", Params{{"root", "//a/../中文/%2F"}}},
				{"/orgs/acme/files/", "/orgs/:org/files/*rest", Params{{"org", "acme"}, {"rest", "/"}}},
				{"/orgs/acme/files//a/../中文/%2F", "/orgs/:org/files/*rest", Params{{"org", "acme"}, {"rest", "//a/../中文/%2F"}}},
				{"/orgs/acme/files/static/7/end", "/orgs/:org/files/static/:id/end", Params{{"org", "acme"}, {"id", "7"}}},
				// 静态分支已提取 id，回退到 catch-all 后必须撤销它。
				{"/orgs/acme/files/static/7/missing", "/orgs/:org/files/*rest", Params{{"org", "acme"}, {"rest", "/static/7/missing"}}},
				{"/orgs/acme/other", "/*root", Params{{"root", "/orgs/acme/other"}}},
			} {
				t.Run(tc.path, func(t *testing.T) {
					params := make(Params, 1, 4)
					params[0] = Param{"keep", "original"}
					route := r.Lookup("GET", tc.path, &params)
					want := append(Params{{"keep", "original"}}, tc.params...)
					if route == nil || route.Path != tc.route || !reflect.DeepEqual(params, want) {
						t.Fatalf("route=%v params=%v; want %s %v", route, params, tc.route, want)
					}
					allocs := testing.AllocsPerRun(100, func() {
						params = params[:1]
						r.Lookup("GET", tc.path, &params)
					})
					if allocs != 0 {
						t.Fatalf("lookup allocated %v times with reusable parameter storage", allocs)
					}
				})
			}
		})
	}
}

func TestRadixProbeMatchesLookup(t *testing.T) {
	testRouterProbeMatchesLookup(t, func() IRouter { return newRadixRouter() })
}

func testRouterProbeMatchesLookup(t *testing.T, newRouter func() IRouter) {
	r := newRouter()
	for _, path := range []string{
		"/", "/users/new/profile", "/users/:id/posts/:post", "/files/*rest", "/files/static/:id/end",
	} {
		r.Insert("GET", path, &Route{Method: "GET", Path: path})
	}
	for _, tc := range []struct {
		method, path, route string
	}{
		{"GET", "/", "/"},
		{"GET", "/users/new/profile", "/users/new/profile"},
		{"GET", "/users/new/posts/7", "/users/:id/posts/:post"},
		{"GET", "/files/", "/files/*rest"},
		{"GET", "/files/static/7/end", "/files/static/:id/end"},
		{"GET", "/files/static/7/missing", "/files/*rest"},
		{"GET", "/users/42/posts/7/missing", ""},
		{"GET", "/users//posts/7", ""},
		{"GET", "/files", ""},
		{"GET", "/missing", ""},
		{"POST", "/files/", ""},
	} {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			params := Params{{"keep", "original"}}
			route := r.Lookup(tc.method, tc.path, &params)
			if probe := r.Lookup(tc.method, tc.path, nil); probe != route {
				t.Fatalf("probe=%v; lookup=%v", probe, route)
			}
			if tc.route == "" {
				if route != nil || !reflect.DeepEqual(params, Params{{"keep", "original"}}) {
					t.Fatalf("miss retained route or parameters: %v %v", route, params)
				}
			} else if route == nil || route.Path != tc.route {
				t.Fatalf("route=%v; want %s", route, tc.route)
			}
			if allocs := testing.AllocsPerRun(100, func() { r.Lookup(tc.method, tc.path, nil) }); allocs != 0 {
				t.Fatalf("probe allocated %v times", allocs)
			}
		})
	}
}
