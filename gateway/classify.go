package gateway

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

// Failure is what went wrong with one attempt, as the layer that saw it can
// tell: the input of a Classifier. It is also an error, so a wire's decoder
// returns one for an error its stream carries, and the stream loop classifies
// it like any other.
type Failure struct {
	// Status is the HTTP status of a refused request, or 0 when the failure
	// was not an HTTP answer: a request that could not be sent, a stream cut
	// off, or an error told inside a 2xx response (an error body, or an error
	// event in the stream).
	Status int
	// Message is the provider's own text where it sent one, or a fixed text
	// of the gateway. It is what callers and Events show, so it never names
	// a host.
	Message string
	// Code and Type are the codes the provider named, either of which may
	// carry the reason alone. Both may be empty.
	Code, Type string
	// Header is the headers of the refusal, nil when there was no response.
	Header http.Header
	// Err is the error under a failure that was not an answer: the transport
	// error of a request that could not be sent, or the read error of a
	// stream cut off. It stays out of the public text.
	Err error
}

// Error implements error with the Message.
func (f Failure) Error() string { return f.Message }

// Unwrap returns Err.
func (f Failure) Unwrap() error { return f.Err }

// Verdict says what a failure means. Retryable: sending the same request
// again may succeed (a rate limit, an overload, a broken stream); the retry
// loop acts on it within Retry.Max, and a Run's model_failure carries it when
// the retries run out. Overflow: the request was longer than the model's
// context window, so sending it again cannot help and compaction should run.
// Limit is the limit in tokens the failure names, or 0 (see Error.ContextLimit).
// An Overflow is never Retryable.
type Verdict struct {
	Retryable, Overflow bool
	Limit               int
}

// Classifier says what a Failure means. DefaultClassifier is the one a Client
// uses unless WithClassifier gives another. A team that only adds to it
// wraps it:
//
//	func(f gateway.Failure) gateway.Verdict {
//		v := gateway.DefaultClassifier(f)
//		if strings.Contains(f.Message, "request_too_big") {
//			v.Overflow, v.Retryable = true, false
//		}
//		return v
//	}
type Classifier func(Failure) Verdict

// DefaultClassifier is the Classifier a Client uses unless WithClassifier says
// otherwise. It holds the gateway's own tables, and nothing in it names a
// provider's address or model:
//
//   - a request that could not be sent, or a stream cut off, is retryable
//     unless its error says that sending again cannot help (a certificate
//     the client does not trust, a host name that does not exist);
//   - a refusal is retryable on 429, 500, 502, 503, 504 and 529, and when its
//     text says "rate limit", "overloaded" or "service unavailable", except a
//     429 that names a quota or balance that is spent;
//   - an error told in a 2xx response is retryable when its code or type is
//     one by which a provider says the failure is its own and may pass
//     (overloaded_error, api_error, rate_limit_error, server_error,
//     rate_limit_exceeded, service_unavailable_error), or by its text as
//     above;
//   - a 400 or 413, or an error told in a 2xx response, whose message, code
//     or type holds one of the phrases by which providers say the request
//     was longer than the model's context window, is an overflow, with the
//     limit its text names.
func DefaultClassifier(f Failure) Verdict {
	var v Verdict
	switch {
	case f.Status != 0:
		v.Retryable = retryableStatus(f.Status, f.Message)
		v.Overflow = (f.Status == http.StatusBadRequest || f.Status == http.StatusRequestEntityTooLarge) &&
			namesOverflow(f.Message, f.Code, f.Type)
	case f.Err != nil && f.Code == "" && f.Type == "":
		v.Retryable = transientTransport(f.Err)
	default:
		v.Retryable = transientStream(f.Code, f.Type) || retryableStatus(0, f.Message)
		v.Overflow = namesOverflow(f.Message, f.Code, f.Type)
	}
	if v.Overflow {
		v.Retryable = false
		v.Limit = limitIn(f.Message)
	}
	return v
}

// statusOverloaded is the non-standard status a provider returns when it
// sheds load.
const statusOverloaded = 529

// retryableStatus is the default retry policy. A 429 that names a terminal
// quota is not retryable; transient 429/5xx responses and known overload
// phrasing are.
func retryableStatus(status int, message string) bool {
	if status == http.StatusTooManyRequests && terminalQuotaLimit(message) {
		return false
	}
	lower := strings.ToLower(message)
	return status == http.StatusTooManyRequests || status == http.StatusInternalServerError ||
		status == http.StatusBadGateway || status == http.StatusServiceUnavailable ||
		status == http.StatusGatewayTimeout || status == statusOverloaded || strings.Contains(lower, "rate limit") ||
		strings.Contains(lower, "overloaded") || strings.Contains(lower, "service unavailable")
}

// terminalQuotaLimit reports whether a 429 message names a quota that
// retrying cannot fix (an exhausted balance or usage cap) rather than a
// transient rate limit.
func terminalQuotaLimit(message string) bool {
	lower := strings.ToLower(message)
	for _, phrase := range []string{"usage limit", "freeusagelimiterror", "gousagelimiterror", "insufficient_quota", "out of budget", "quota exceeded", "available balance"} {
		if strings.Contains(lower, phrase) {
			return true
		}
	}
	return false
}

// transientStreamKinds are the types and codes by which a provider, in an
// error it sends in the middle of a stream, says the failure is its own and
// may pass: Anthropic's overloaded_error, api_error and rate_limit_error,
// and the server_error and rate_limit_exceeded of the OpenAI wires. A stream
// that started is never sent again by the gateway unless a Session listens
// (see the stream loop): the error is marked Retryable for the caller, which
// can run the Turn again.
var transientStreamKinds = map[string]bool{
	"overloaded_error":          true,
	"api_error":                 true,
	"rate_limit_error":          true,
	"server_error":              true,
	"rate_limit_exceeded":       true,
	"service_unavailable_error": true,
}

// transientStream reports whether any of the types and codes of a stream
// error says the failure may pass.
func transientStream(kinds ...string) bool {
	for _, kind := range kinds {
		if transientStreamKinds[strings.ToLower(strings.TrimSpace(kind))] {
			return true
		}
	}
	return false
}

// transientTransport reports whether a failed send may succeed when sent
// again. A certificate the client does not trust or that does not fit the
// host, and a host name that does not exist, are mistakes in the settings
// and stay the same on every attempt.
func transientTransport(err error) bool {
	var (
		unknownAuthority x509.UnknownAuthorityError
		invalid          x509.CertificateInvalidError
		hostname         x509.HostnameError
		verification     *tls.CertificateVerificationError
		dns              *net.DNSError
		invalidAddr      net.InvalidAddrError
		unknownNetwork   net.UnknownNetworkError
	)
	switch {
	case errors.As(err, &unknownAuthority), errors.As(err, &invalid), errors.As(err, &hostname),
		errors.As(err, &verification), errors.As(err, &invalidAddr), errors.As(err, &unknownNetwork):
		return false
	case errors.As(err, &dns):
		return !dns.IsNotFound
	}
	return true
}

// overflowPhrases are the fragments, lower case, by which a provider's error
// body says the request was longer than the model's context window. They are
// the only place the gateway reads that out of prose: providers publish no
// stable code for it on every wire API. They are matched against an error's
// message, code and type. The list is short on purpose. internal/fakegateway
// plays each provider's wording, which is the providers' documentation and
// wording reports, not a live provider; a provider that words it differently
// is reported as an ordinary failure, unless the team wraps DefaultClassifier
// with its own wording.
var overflowPhrases = []string{
	"prompt is too long",            // Anthropic Messages
	"exceed context limit",          // Anthropic Messages, input plus max_tokens
	"maximum context length",        // OpenAI Chat Completions and compatible servers
	"context_length_exceeded",       // OpenAI error code
	"exceeds the context window",    // OpenAI Responses and others
	"input is too long",             // gateways in front of other providers
	"model_context_window_exceeded", // Anthropic Messages, the window ran out during the reply
}

// limitPatterns are the wordings, lower case, by which a provider's refusal
// names the limit of the request in tokens. The first group of each is the
// number. A refusal in other words names none, and the compaction then sizes
// its work to the Segments' budget instead.
var limitPatterns = []*regexp.Regexp{
	regexp.MustCompile(`maximum context length is (\d[\d,]*)`), // OpenAI Chat Completions and compatible servers
	regexp.MustCompile(`> (\d[\d,]*) maximum`),                 // Anthropic Messages: "prompt is too long: N tokens > M maximum"
	regexp.MustCompile(`\+ [\d,]+ > (\d[\d,]*)`),               // Anthropic Messages, input plus max_tokens
	regexp.MustCompile(`context window of (\d[\d,]*)`),
}

// limitIn returns the token limit a refusal's message names, or 0.
func limitIn(message string) int {
	lower := strings.ToLower(message)
	for _, pattern := range limitPatterns {
		match := pattern.FindStringSubmatch(lower)
		if match == nil {
			continue
		}
		if limit, err := strconv.Atoi(strings.ReplaceAll(match[1], ",", "")); err == nil && limit > 0 {
			return limit
		}
	}
	return 0
}

// namesOverflow reports whether any of the texts holds an overflow phrase.
func namesOverflow(texts ...string) bool {
	for _, text := range texts {
		lower := strings.ToLower(text)
		for _, phrase := range overflowPhrases {
			if strings.Contains(lower, phrase) {
				return true
			}
		}
	}
	return false
}
