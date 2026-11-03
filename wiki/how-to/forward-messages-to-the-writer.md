# Forward messages to the writer

> [Wiki](../README.md) › [How-to](../README.md#how-to) › Forward messages to the writer

By the end of this page a message that reaches the wrong Instance is answered with the Writer's address, or forwarded to it. You need [HTTP in five minutes](../tutorials/http-in-five-minutes.md).

## Name the writer, or forward to it

A Session has one Writer at a time. With several Instances behind a load balancer, a message can reach an Instance that is not the one running the Session. That Instance answers 409 `not_writer` and names the Writer, and nothing ran.

For the Instance to name an address, give each one `host.Config.Address`: where a caller can reach it.

```go
runner, err := host.New(host.Config{
	Store:        store.Store{Records: st, Liveness: st},
	Instance:     "pod-a",                // a label; each start adds a random suffix
	Address:      "http://10.0.0.7:8080", // advertised in the Liveness row
	Profiles:     profiles,
	DefaultAgent: "default",
})
```

Here is what a second Instance answers while the first runs the Session (a run of two Instances on one shared Store, verified):

```text
POST pod-b/v1/sessions/ID/messages  {"text":"also check X"}
409  Gotato-Not-Run: true   Gotato-Retryable: true   Gotato-Writer: http://127.0.0.1:58129
{"code":"not_writer","message":"not_writer: host: the session's run is going on another instance (http://127.0.0.1:58129)","not_run":true,"retryable":true,"writer":"http://127.0.0.1:58129"}
```

A client can resend to the `Gotato-Writer` address itself. Or the handler forwards the message once, with `WithForwarding`:

```go
allow := func(address string) bool {
	u, err := url.Parse(address)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}
	ip, err := netip.ParseAddr(u.Hostname()) // a host name is refused
	return err == nil && netip.MustParsePrefix("10.0.0.0/16").Contains(ip)
}
handler := httpapi.New(runner, httpapi.WithForwarding(nil, allow))
```

- `WithForwarding(client, allow)` takes an HTTP client (`nil` uses one with a 30 second timeout) and an allow function.
- The address comes from the Store, and anyone who can write that row can name it. A forward goes only to an address for which `allow` returns true; for any other, the answer is `not_writer`, as without forwarding.
- A nil `allow` forwards nowhere. Pass the check you trust: the host is an IP literal inside your cluster's CIDR (as above; never a string prefix, which `http://10.0.evil.com` passes), or a lookup in your own list of Instances.
- A forwarded request carries the caller's headers, so your authentication runs at the Writer. It carries `Gotato-Forwarded: true`, and the Writer never forwards it again.
- Only a message, a notice and an abort are forwarded. A `/runs` request for a Session that another Instance is running does not answer `not_writer`: it waits for that Run to end, for at most `host.Heartbeat.Wait` (5 seconds unless set), then runs on this Instance, or is refused `busy` (409, not run, retryable). Send Runs through your load balancer, which hashes the `session-id` header so every request for one Session reaches one Instance.

With forwarding on, the same message to the second Instance answers the Writer's own `202 {"session_id":...,"queued":true}`.

## See also

- [How a message misses the writer](../../specs/host.md)
- [How forwarding works](../../specs/http.md)
- [Forwarding, as the user sees it](../../FEATURES.md#f125-forward-to-the-writer)
