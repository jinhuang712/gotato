package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
)

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
