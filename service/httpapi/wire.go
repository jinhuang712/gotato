package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

// defaultMaxBody is the default bound of every JSON request body (WithMaxBody
// changes it). A larger body is rejected with 413 rather than being buffered.
const defaultMaxBody = 1 << 20

// errBodyTooLarge marks a request body that exceeded the Handler's bound.
var errBodyTooLarge = errors.New("request body too large")

// readBody reads a request body, at most the Handler's bound of it.
func (h *Handler) readBody(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	if r.Body == nil {
		return nil, nil
	}
	// [SECURITY] a client cannot make the handler read a body past the cap. Test: TestOversizedBodyIsRefusedOnMessageAndNoticeRoutes
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, h.maxBody))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return nil, errBodyTooLarge
		}
		return nil, err
	}
	return body, nil
}

func (h *Handler) decode(w http.ResponseWriter, r *http.Request, into any) error {
	if r.Body == nil {
		return nil
	}
	// [SECURITY] a client cannot make the handler read a body past the cap. Test: TestWithMaxBodyRaisesAndLowersTheCap
	r.Body = http.MaxBytesReader(w, r.Body, h.maxBody)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	err := decoder.Decode(into)
	if errors.Is(err, io.EOF) {
		// An absent body, including a chunked request with no data, is not
		// malformed JSON.
		return nil
	}
	if err == nil {
		// [CONTRACT] a request is not run on a body that says more than it was read for. Test: TestABodyWithTrailingDataIsRefused
		// The body is one value: anything after it but white space is
		// refused, so a request is not run on a body that says more than it
		// was read for.
		var next json.RawMessage
		switch err = decoder.Decode(&next); {
		case errors.Is(err, io.EOF):
			return nil
		case err == nil:
			err = errors.New("data after the JSON value")
		}
	}
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		return errBodyTooLarge
	}
	return fmt.Errorf("invalid JSON body: %w", err)
}

// writeDecodeError reports a request-parsing failure: 413 for an oversized
// body, 400 for everything else.
func writeDecodeError(w http.ResponseWriter, err error) {
	if errors.Is(err, errBodyTooLarge) {
		writeError(w, http.StatusRequestEntityTooLarge, err)
		return
	}
	writeError(w, http.StatusBadRequest, err)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(value)
}
