package engine

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestJSONBindingLimitsAndStrictness(t *testing.T) {
	for _, tt := range []struct {
		name, body string
		limit      int64
		strict     bool
		code       int
	}{
		{"valid", `{"name":"alice"}`, 64, false, 200},
		{"oversized", `{"name":"` + strings.Repeat("x", 80) + `"}`, 64, false, 413},
		{"chunked oversized", `{"name":"` + strings.Repeat("x", 80) + `"}`, 64, false, 413},
		{"multiple values", `{"name":"alice"} {}`, 64, false, 400},
		{"trailing garbage", `{"name":"alice"} x`, 64, false, 400},
		{"trailing whitespace exceeds budget", `{"name":"alice"}` + strings.Repeat(" ", 80), 64, false, 413},
		{"unknown allowed", `{"name":"alice","extra":1}`, 64, false, 200},
		{"unknown rejected", `{"name":"alice","extra":1}`, 64, true, 400},
		{"unlimited", `{"name":"` + strings.Repeat("x", 128) + `"}`, 0, false, 200},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e := New()
			e.MaxBodyBytes = tt.limit
			e.DisallowUnknownJSONFields = tt.strict
			e.POST("/", func(c *Context) {
				var value struct {
					Name string `json:"name"`
				}
				if err := c.BindJSON(&value); err != nil {
					var tooLarge *http.MaxBytesError
					if errors.As(err, &tooLarge) {
						c.AbortWithStatus(413)
					} else {
						c.AbortWithStatus(400)
					}
					return
				}
				c.JSON(200, value)
			})
			req := httptest.NewRequest("POST", "/", strings.NewReader(tt.body))
			if tt.name == "chunked oversized" {
				req.ContentLength = -1
			}
			w := httptest.NewRecorder()
			e.ServeHTTP(w, req)
			if w.Code != tt.code {
				t.Fatalf("status=%d want=%d body=%s", w.Code, tt.code, w.Body.String())
			}
		})
	}
}

func TestCustomJSONAndEnvelope(t *testing.T) {
	e := New()
	e.MaxBodyBytes = 8
	e.JSONBinder = func(r io.Reader, v any, _ bool) error {
		data, err := io.ReadAll(r)
		if err != nil {
			return err
		}
		*v.(*string) = string(data)
		return nil
	}
	e.JSONRenderer = func(w http.ResponseWriter, code int, v any) error {
		w.Header().Set("Content-Type", "custom/json")
		return DefaultJSONRenderer(w, code, map[string]any{"custom": v})
	}
	e.EnvelopeBuilder = func(code int, message string, data any) any { return map[string]any{"code": code, "payload": data} }
	e.POST("/", func(c *Context) {
		var value string
		if err := c.BindJSON(&value); err != nil {
			c.AbortWithStatus(413)
			return
		}
		c.AjaxSuccess("ok", value)
	})
	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest("POST", "/", strings.NewReader("hello")))
	var got map[string]map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || got["custom"]["payload"] != "hello" {
		t.Fatalf("custom adapters: %s %v", w.Body.String(), err)
	}
	w = httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest("POST", "/", strings.NewReader("too large")))
	if w.Code != 413 {
		t.Fatal("custom binder bypassed body limit")
	}
}

type bindingSchema struct {
	Name              string `json:"name"`
	validated, hooked bool
}

func (s *bindingSchema) Valid() error {
	s.validated = true
	if s.Name == "" {
		return errors.New("missing name")
	}
	return nil
}
func (s *bindingSchema) Hook() { s.hooked = true }
func TestJSONArgsValidationOrder(t *testing.T) {
	for _, body := range []string{`{"name":"alice"}`, `{}`, `{"name":"alice"} {}`} {
		c := NewContext(httptest.NewRecorder(), httptest.NewRequest("POST", "/", strings.NewReader(body)))
		var value bindingSchema
		err := c.JSONArgs(&value)
		if body == `{"name":"alice"}` {
			if err != nil || !value.validated || !value.hooked {
				t.Fatal("valid schema did not run validation and hook")
			}
		} else if err == nil || value.hooked {
			t.Fatal("invalid schema hook ran")
		}
	}
}

func TestRequestBodyLimitAlsoAppliesToRawReaders(t *testing.T) {
	e := New()
	e.MaxBodyBytes = 8
	e.POST("/", func(c *Context) {
		_, err := io.ReadAll(c.Request().Body)
		var tooLarge *http.MaxBytesError
		if !errors.As(err, &tooLarge) {
			t.Errorf("raw body limit missing: %v", err)
		}
		c.AbortWithStatus(413)
	})
	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest("POST", "/", strings.NewReader("more than eight bytes")))
	if w.Code != 413 {
		t.Fatal(w.Code)
	}
}
