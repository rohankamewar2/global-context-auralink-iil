package main

import "testing"

func TestLocalCheckNeverSendsSecretsToPublicOrRedirectTargets(t *testing.T) {
	for _, url := range []string{"https://example.com", "http://example.com", "http://127.0.0.1.evil.test", "http://user:pass@127.0.0.1", "http://127.0.0.1?x=1", "http://127.0.0.1?", "http://127.0.0.1/path", "http://127.0.0.1#fragment"} {
		if localURL(url) {
			t.Fatal("checker accepted a non-loopback or ambiguous target")
		}
	}
	for _, url := range []string{"http://127.0.0.1:8082", "http://localhost:8082", "http://[::1]:8082"} {
		if !localURL(url) {
			t.Fatal("checker rejected loopback target")
		}
	}
}
