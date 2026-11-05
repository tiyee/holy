package engine

import (
	"context"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

type Context struct {
	ctx             context.Context
	w               http.ResponseWriter
	r               *http.Request
	handlers        []HandlerFunc
	fallbackHandler HandlerFunc
	index           int
	aborted         bool
	userData        map[string]any
	queryParsed     bool
	queryMap        url.Values
	domain          string
	sameSite        http.SameSite
	writer          responseWriter
	Params          Params
	fullPath        string
	maxBodyBytes    int64
	strictJSON      bool
	jsonBinder      JSONBinder
	jsonRenderer    JSONRenderer
	envelope        EnvelopeBuilder
}

const abortIndex = math.MaxInt8 >> 1

func NewContext(w http.ResponseWriter, r *http.Request) *Context {
	c := &Context{}
	c.reset(w, r)
	return c
}

func (c *Context) Request() *http.Request {

	return c.r
}
func (c *Context) Response() http.ResponseWriter {
	return c.w
}
func (c *Context) Ctx() context.Context {
	return c.ctx
}
func (c *Context) JSONArgs(v Schema) error {
	if err := c.BindJSON(v); err != nil {
		return err
	}
	if err := v.Valid(); err != nil {
		return err
	}
	v.Hook()
	return nil
}
func (c *Context) String(code int, text string) {
	c.Response().Header().Set("Content-Type", "text/plain; charset=utf-8")
	c.Response().WriteHeader(code)
	c.Response().Write([]byte(text))

}
func (c *Context) AjaxJson(code int, message string, data any) {
	c.JSON(http.StatusOK, c.envelope(code, message, data))
}
func (c *Context) AjaxError(code int, message string, data any) {
	c.AjaxJson(code, message, data)
}
func (c *Context) AjaxSuccess(message string, data any) {
	c.AjaxJson(0, message, data)
}

// JSON 在写入响应头之前完成编码，编码失败返回 500。
func (c *Context) JSON(httpCode int, data any) {
	if err := c.jsonRenderer(c.Response(), httpCode, data); err != nil {
		if !c.Written() {
			c.Error("JSON encoding error", http.StatusInternalServerError)
		}
		c.Abort()
	}
}

// BindJSON 默认限制请求体并拒绝尾随 JSON；严格未知字段策略可在引擎上配置。
func (c *Context) BindJSON(v any) error {
	reader := c.r.Body
	if c.maxBodyBytes > 0 {
		reader = http.MaxBytesReader(c.w, reader, c.maxBodyBytes)
	}
	return c.jsonBinder(reader, v, c.strictJSON)
}
func (c *Context) Param(key string) string { return c.Params.ByName(key) }
func (c *Context) FullPath() string        { return c.fullPath }
func (c *Context) Status() int             { return c.writer.status }
func (c *Context) Written() bool           { return c.writer.Written() }

func (c *Context) Next() {
	if c.IsAborted() {
		return
	}
	c.index++
	for c.index < len(c.handlers) {
		c.handlers[c.index](c)
		c.index++
	}
}

// IsAborted 表示当前请求是否被显式中止。
func (c *Context) IsAborted() bool {
	return c.aborted
}
func (c *Context) Abort() {
	c.aborted = true
	c.index = abortIndex
}
func (c *Context) reset(w http.ResponseWriter, r *http.Request) {
	c.r = r
	c.maxBodyBytes = DefaultMaxBodyBytes
	c.strictJSON = false
	c.jsonBinder = DefaultJSONBinder
	c.jsonRenderer = DefaultJSONRenderer
	c.envelope = defaultEnvelope
	c.writer.reset(w)
	c.w = &c.writer
	c.handlers = nil
	c.index = -1
	c.aborted = false
	c.queryParsed = false
	c.queryMap = nil
	clear(c.userData)
	c.domain = ""
	c.sameSite = http.SameSiteNoneMode
	clear(c.Params)
	c.Params = c.Params[:0]
	c.fullPath = ""
	c.ctx = r.Context()
}
func (c *Context) Error(message string, code int) {
	http.Error(c.w, message, code)
}
func (c *Context) NotFound() {
	c.Error("not found", 404)
}
func (c *Context) Redirect(code int, url string) {
	http.Redirect(c.w, c.r, url, code)
	c.Abort()
	return
}
func (c *Context) SetUserValue(key string, value interface{}) {
	if c.userData == nil {
		c.userData = make(map[string]any)
	}
	c.userData[key] = value
}
func (c *Context) UserValue(key string) any {
	if v, exist := c.userData[key]; exist {
		return v
	} else {
		return nil
	}
}
func (c *Context) SetDomain(domain string) {
	c.domain = domain
}

// SetSameSite with cookie
func (c *Context) SetSameSite(samesite http.SameSite) {
	c.sameSite = samesite
}

func (c *Context) SetCookie(key string, value []byte, expired time.Duration) {
	cookie := &http.Cookie{
		Domain:   c.domain,
		HttpOnly: true,
		Path:     "/",
		Name:     key,
		Expires:  time.Now().Add(expired),
		Secure:   true,
		Value:    string(value),
		SameSite: c.sameSite,
	}
	http.SetCookie(c.w, cookie)
}
func (c *Context) SetCookieV2(cookie *http.Cookie) {
	if cookie != nil && cookie.SameSite == http.SameSiteDefaultMode {
		scoped := *cookie
		scoped.SameSite = c.sameSite
		cookie = &scoped
	}
	http.SetCookie(c.w, cookie)
}
func (c *Context) SetAuthorization(s string) {
	c.Response().Header().Set("Authorization", s)
}
func (c *Context) Authorization() string {
	return c.Request().Header.Get("Authorization")
}
func (c *Context) Cookie(key string) string {
	if cookie, err := c.Request().Cookie(key); err == nil {
		return cookie.Value
	} else {
		return ""
	}
}
func (c *Context) initQueryCache() {
	if !c.queryParsed {
		c.queryMap = c.r.URL.Query()
		c.queryParsed = true
	}
}

func (c *Context) Query(key string) string {
	c.initQueryCache()
	return c.queryMap.Get(key)
}
func (c *Context) QueryInt(key string, missing int) int {
	return int(c.QueryInt64(key, int64(missing)))
}
func (c *Context) QueryInt64(key string, missing int64) int64 {
	c.initQueryCache()
	s := c.queryMap.Get(key)
	if len(s) == 0 {
		return missing
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return n
	}
	return missing
}
func (c *Context) QueryArray(key string) []string {
	c.initQueryCache()
	if array, exist := c.queryMap[key]; exist {
		return array
	} else {
		return []string{}
	}
}
func (c *Context) QueryMap() url.Values {
	c.initQueryCache()
	return c.queryMap

}
func (c *Context) PostForm(key string) string {
	return c.r.PostFormValue(key)
}
func (c *Context) AbortWithStatus(code int) {
	c.w.WriteHeader(code)
	c.Abort()
}

// release 清除请求引用，避免对象池延长请求与响应内容的生命周期。
func (c *Context) release() {
	c.r = nil
	c.w = nil
	c.ctx = nil
	c.handlers = nil
	c.fallbackHandler = nil
	c.queryMap = nil
	clear(c.userData)
	clear(c.Params[:cap(c.Params)])
	c.Params = c.Params[:0]
	c.fullPath = ""
	c.domain = ""
	c.jsonBinder = nil
	c.jsonRenderer = nil
	c.envelope = nil
	c.writer.ResponseWriter = nil
}
