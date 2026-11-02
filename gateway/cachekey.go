package gateway

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
)

// cacheKeyFieldNone is the CacheKeyField that sends no field, for a wire whose
// default is one.
const cacheKeyFieldNone = "none"

// checkCacheKeyField refuses a cache key field that equals a field the
// request already sends: it would silently replace it. The fields of the
// encoder's own struct are read from its json tags, so a field added to the
// encoder is reserved with no second list; reserved names the fields the API
// takes besides. [CONTRACT] a settings mistake is reported by New, before the
// first Stream.
func checkCacheKeyField(field string, request reflect.Type, reserved []string) error {
	field = strings.TrimSpace(field)
	if field == "" || field == cacheKeyFieldNone {
		return nil
	}
	for i := range request.NumField() {
		tag, _, _ := strings.Cut(request.Field(i).Tag.Get("json"), ",")
		if strings.EqualFold(tag, field) {
			return fmt.Errorf("gateway: CacheKeyField %q is a field the request already sends", field)
		}
	}
	for _, name := range reserved {
		if strings.EqualFold(name, field) {
			return fmt.Errorf("gateway: CacheKeyField %q is a field the request already sends", field)
		}
	}
	return nil
}

// withCacheKeyField adds the request's cache key to an encoded body as the
// top-level field. It appends the field to the object, so the fields before it
// keep their order.
func withCacheKeyField(body []byte, field, key string) ([]byte, error) {
	field = strings.TrimSpace(field)
	if field == "" || field == cacheKeyFieldNone || key == "" {
		return body, nil
	}
	// [IMPORTANT] the field is appended before the closing brace, so a body that is not an object would come out as corrupt JSON.
	if len(body) < 2 || body[0] != '{' || body[len(body)-1] != '}' {
		return nil, errors.New("gateway: the encoded request is not a JSON object")
	}
	name, err := json.Marshal(field)
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
