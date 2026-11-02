package http_test

import (
	"net/netip"
	"net/url"
	"testing"
)

// allowClusterAddress is the allow check shown in guides/http-in-five-minutes.md:
// an http or https URL whose host is an IP literal inside the cluster's CIDR.
// A host name is refused, so "http://10.0.evil.com" never passes.
func allowClusterAddress(address string) bool {
	u, err := url.Parse(address)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}
	ip, err := netip.ParseAddr(u.Hostname())
	return err == nil && netip.MustParsePrefix("10.0.0.0/16").Contains(ip)
}

func TestAllowClusterAddress(t *testing.T) {
	allowed := []string{"http://10.0.1.5:8080", "https://10.0.0.7"}
	refused := []string{
		"http://10.0.evil.com", "http://10.0.evil.com:8080", "http://10.1.0.1:8080",
		"ftp://10.0.1.5", "10.0.1.5:8080", "http://user@10.0.evil.com", "", "http://[::1]:80",
	}
	for _, a := range allowed {
		if !allowClusterAddress(a) {
			t.Errorf("%q refused, want allowed", a)
		}
	}
	for _, a := range refused {
		if allowClusterAddress(a) {
			t.Errorf("%q allowed, want refused", a)
		}
	}
}
