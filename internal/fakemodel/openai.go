package fakemodel

import (
	"net/http"
	"strings"
)

// The error form, the fake's own refusal and the auth check the two OpenAI
// wire APIs share.

func openAIErr(status int, message, param, code string) *apiError {
	e := map[string]any{"message": message, "type": "invalid_request_error", "param": nil, "code": nil}
	if param != "" {
		e["param"] = param
	}
	if code != "" {
		e["code"] = code
	}
	return &apiError{status: status, body: map[string]any{"error": e}}
}

// openAIWire is what the two OpenAI wire APIs share.
type openAIWire struct{}

// invalid wraps a message of the fake's own in OpenAI's error form.
func (openAIWire) invalid(message string) *apiError {
	return openAIErr(http.StatusBadRequest, message, "", "")
}

func openAIAuthorize(h http.Header) *apiError {
	if !strings.HasPrefix(h.Get("Authorization"), "Bearer ") || strings.TrimSpace(strings.TrimPrefix(h.Get("Authorization"), "Bearer ")) == "" {
		e := openAIErr(http.StatusUnauthorized, "You didn't provide an API key. You need to provide your API key in an Authorization header using Bearer auth (i.e. Authorization: Bearer YOUR_KEY).", "", "")
		return e
	}
	return nil
}
