package engine

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

const DefaultMaxBodyBytes int64 = 1 << 20

// JSONBinder 接收受请求体预算限制的 Reader；strict 控制未知字段策略。
// 自定义实现必须支持并发请求，且不得保留请求 Reader。
type JSONBinder func(reader io.Reader, destination any, strict bool) error

// JSONRenderer 应在写入响应头前完成编码，编码或写入失败时返回错误。
type JSONRenderer func(http.ResponseWriter, int, any) error

// EnvelopeBuilder 可替换兼容 Ajax 方法的业务响应结构。
type EnvelopeBuilder func(code int, message string, data any) any

// Schema 通过结构化接口保留校验及 Hook，核心不依赖业务 schema 包。
type Schema interface {
	Valid() error
	Hook()
}

func DefaultJSONBinder(reader io.Reader, destination any, strict bool) error {
	decoder := json.NewDecoder(reader)
	if strict {
		decoder.DisallowUnknownFields()
	}
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err != nil {
			return err
		}
		return errors.New("holy: request body must contain one JSON value")
	}
	return nil
}

func DefaultJSONRenderer(w http.ResponseWriter, code int, data any) error {
	encoded, err := json.Marshal(data)
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_, err = w.Write(encoded)
	return err
}

func defaultEnvelope(code int, message string, data any) any {
	return struct {
		Error   int    `json:"error"`
		Message string `json:"message"`
		Msg     string `json:"msg"`
		Data    any    `json:"data"`
	}{code, message, message, data}
}
