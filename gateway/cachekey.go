package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
)

// reservedBodyFields are the top-level fields the wire API's request carries
// besides the ones its encoder struct declares: fields a provider takes that
// a cache key must not replace either.
var reservedBodyFields = map[API][]string{
	APIChatCompletions: {"tool_choice", "parallel_tool_calls", "max_completion_tokens", "response_format", "stop", "n", "user", "metadata", "store"},
	APIResponses:       {"max_tool_calls", "metadata", "text", "truncation", "user"},
}

// reservedBodyField reports whether name is a top-level field the request of
// api already sends. The fields of the encoder's own struct are read from its
// json tags, so a field added to the encoder is reserved with no second list.
func reservedBodyField(api API, name string) bool {
	if name == "" {
		return false
	}
	var request reflect.Type
	switch api {
	case APIChatCompletions:
		request = reflect.TypeFor[chatRequest]()
	case APIResponses:
		request = reflect.TypeFor[responsesRequest]()
	default:
		return false
	}
	for i := range request.NumField() {
		tag, _, _ := strings.Cut(request.Field(i).Tag.Get("json"), ",")
		if strings.EqualFold(tag, name) {
			return true
		}
	}
	for _, reserved := range reservedBodyFields[api] {
		if strings.EqualFold(reserved, name) {
			return true
		}
	}
	return false
}

// opener sends one encoded request and returns the provider's response.
type opener func(context.Context, []byte) (*http.Response, error)

// withCacheKeyHeader returns open with the request's cache key attached as
// the configured header. The restarter keeps the returned opener, so a
// restarted stream carries the key too.
func (c *Client) withCacheKeyHeader(open opener, key string) opener {
	if c.cacheKeyHeader == "" || key == "" {
		return open
	}
	headers := map[string]string{c.cacheKeyHeader: key}
	return func(ctx context.Context, body []byte) (*http.Response, error) {
		return open(WithRequestHeaders(ctx, headers), body)
	}
}

// withCacheKeyField adds the request's cache key to an encoded body as the
// configured top-level field. It appends the field to the object, so the
// fields before it keep their order.
func (c *Client) withCacheKeyField(body []byte, key string) ([]byte, error) {
	if c.cacheKeyField == "" || key == "" {
		return body, nil
	}
	if len(body) < 2 || body[0] != '{' || body[len(body)-1] != '}' {
		return nil, errors.New("gateway: the encoded request is not a JSON object")
	}
	if reservedBodyField(c.api, c.cacheKeyField) {
		return nil, fmt.Errorf("gateway: CacheKeyField %q is a field the request already sends", c.cacheKeyField)
	}
	name, err := json.Marshal(c.cacheKeyField)
	if err != nil {
		return nil, err
	}
	value, err := json.Marshal(key)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, len(body)+len(name)+len(value)+2)
	out = append(out, body[:len(body)-1]...)
	if len(body) > 2 {
		out = append(out, ',')
	}
	out = append(out, name...)
	out = append(out, ':')
	out = append(out, value...)
	return append(out, '}'), nil
}
