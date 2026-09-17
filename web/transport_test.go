package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

type staticResolver map[string][]string

func (r staticResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	addresses, ok := r[host]
	if !ok {
		return nil, fmt.Errorf("no such host %q", host)
	}
	out := make([]netip.Addr, 0, len(addresses))
	for _, address := range addresses {
		parsed, err := netip.ParseAddr(address)
		if err != nil {
			return nil, err
		}
		out = append(out, parsed)
	}
	return out, nil
}

func TestParseFetchURLRejectsUnsafeForms(t *testing.T) {
	policy := Policy{}.withDefaults()
	cases := []struct {
		name string
		url  string
		code string
	}{
		{"scheme", "ftp://example.com/file", CodeInvalidURL},
		{"file", "file:///etc/passwd", CodeInvalidURL},
		{"gopher", "gopher://example.com", CodeInvalidURL},
		{"credentials", "https://user:pass@example.com/", CodeBlockedURL},
		{"too long", "https://example.com/" + strings.Repeat("a", 3000), CodeInvalidURL},
		{"no host", "http://", CodeInvalidURL},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := parseFetchURL(testCase.url, policy)
			if got := ErrorCode(err); got != testCase.code {
				t.Fatalf("code = %q, want %q (err=%v)", got, testCase.code, err)
			}
		})
	}
}

func TestIsPublicAddressBlocksNonReachableRanges(t *testing.T) {
	blocked := []string{
		"127.0.0.1", "10.0.0.1", "172.16.0.1", "192.168.1.1", "169.254.169.254",
		"0.0.0.0", "100.64.0.1", "192.0.0.170", "198.18.0.1", "198.51.100.4",
		"203.0.113.7", "240.0.0.1", "224.0.0.1",
		"::1", "fe80::1", "fc00::1", "fd12::1", "ff02::1",
		"::ffff:127.0.0.1", "::ffff:10.0.0.1",
		"2002:7f00:1::", "2001::1", "64:ff9b::7f00:1", "2001:db8::1",
	}
	for _, address := range blocked {
		parsed, err := netip.ParseAddr(address)
		if err != nil {
			t.Fatalf("ParseAddr(%s) returned error: %v", address, err)
		}
		if isPublicAddress(parsed) {
			t.Fatalf("%s was classified public", address)
		}
	}

	public := []string{"8.8.8.8", "1.1.1.1", "93.184.216.34", "2606:4700::1111", "::ffff:8.8.8.8"}
	for _, address := range public {
		parsed, err := netip.ParseAddr(address)
		if err != nil {
			t.Fatalf("ParseAddr(%s) returned error: %v", address, err)
		}
		if !isPublicAddress(parsed) {
			t.Fatalf("%s was classified non-public", address)
		}
	}
}

func TestResolvePublicRejectsMixedAndPrivateAnswers(t *testing.T) {
	ctx := context.Background()
	resolver := staticResolver{
		"mixed.example":   {"93.184.216.34", "127.0.0.1"},
		"private.example": {"10.1.2.3"},
		"public.example":  {"93.184.216.34", "2606:4700::1111"},
		"empty.example":   {},
	}
	policy := Policy{Resolver: resolver}

	if _, err := ResolvePublic(ctx, "mixed.example", policy); ErrorCode(err) != CodeBlockedURL {
		t.Fatalf("mixed answers were accepted: %v", err)
	}
	if _, err := ResolvePublic(ctx, "private.example", policy); ErrorCode(err) != CodeBlockedURL {
		t.Fatalf("private answers were accepted: %v", err)
	}
	if _, err := ResolvePublic(ctx, "empty.example", policy); ErrorCode(err) != CodeProviderError {
		t.Fatalf("empty answers were accepted: %v", err)
	}
	addresses, err := ResolvePublic(ctx, "public.example", policy)
	if err != nil || len(addresses) != 2 {
		t.Fatalf("public addresses = %v err=%v", addresses, err)
	}

	// A literal address needs no resolution but is still classified.
	if _, err := ResolvePublic(ctx, "169.254.169.254", policy); ErrorCode(err) != CodeBlockedURL {
		t.Fatalf("link-local literal was accepted: %v", err)
	}
	if _, err := ResolvePublic(ctx, "8.8.8.8", policy); err != nil {
		t.Fatalf("public literal was rejected: %v", err)
	}
	if _, err := ResolvePublic(ctx, "not-a-real-host.invalid", policy); ErrorCode(err) != CodeProviderError {
		t.Fatalf("unresolvable host error = %v", err)
	}
}

func TestFetchBlocksLoopbackWithoutAllowPrivate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("<html><body>secret</body></html>"))
	}))
	defer server.Close()

	_, err := Fetch(context.Background(), server.URL, Policy{})
	if ErrorCode(err) != CodeBlockedURL {
		t.Fatalf("loopback fetch error = %v, want WEB_BLOCKED_URL", err)
	}
	if err != nil && strings.Contains(err.Error(), "secret") {
		t.Fatal("blocked fetch leaked body content")
	}
}

func TestFetchFollowsSameOriginRedirectsAndRendersHTML(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/start", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/final", http.StatusFound)
	})
	mux.HandleFunc("/final", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<html><head><title>t</title><script>bad()</script></head><body><h1>Title</h1><p>Hello   world</p><a href=\"https://example.com/x\">link</a></body></html>"))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	result, err := Fetch(context.Background(), server.URL+"/start", Policy{AllowPrivate: true})
	if err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}
	if result.StatusCode != 200 {
		t.Fatalf("status = %d", result.StatusCode)
	}
	for _, want := range []string{"Title", "Hello world", "https://example.com/x"} {
		if !strings.Contains(result.Body, want) {
			t.Fatalf("body %q does not contain %q", result.Body, want)
		}
	}
	for _, unwanted := range []string{"bad()", "<h1>", "<script>"} {
		if strings.Contains(result.Body, unwanted) {
			t.Fatalf("body %q leaked %q", result.Body, unwanted)
		}
	}
}

func TestFetchRefusesCrossOriginRedirectsAndHopOverflow(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer target.Close()

	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/elsewhere", http.StatusFound)
	}))
	defer redirector.Close()

	_, err := Fetch(context.Background(), redirector.URL, Policy{AllowPrivate: true})
	if ErrorCode(err) != CodeRedirectBlocked {
		t.Fatalf("cross-origin redirect error = %v", err)
	}
	if !strings.Contains(err.Error(), "retry against that URL directly") {
		t.Fatalf("error text = %q", err.Error())
	}

	// A chain of distinct paths exercises the hop cap; a self-redirect is
	// reported as a loop instead (a stricter check than the reference).
	chain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/hop"+strings.TrimPrefix(r.URL.Path, "/hop")+"x", http.StatusFound)
	}))
	defer chain.Close()
	_, err = Fetch(context.Background(), chain.URL+"/hop", Policy{AllowPrivate: true, MaxRedirects: Redirects(2)})
	if ErrorCode(err) != CodeRedirectBlocked || !strings.Contains(err.Error(), "maximum of 2 redirects") {
		t.Fatalf("hop overflow error = %v", err)
	}

	loop := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/again", http.StatusFound)
	}))
	defer loop.Close()
	_, err = Fetch(context.Background(), loop.URL+"/again", Policy{AllowPrivate: true, MaxRedirects: Redirects(2)})
	if ErrorCode(err) != CodeRedirectBlocked || !strings.Contains(err.Error(), "redirect loop detected") {
		t.Fatalf("loop error = %v", err)
	}
}

func TestFetchEnforcesContentTypeAndSizeLimits(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/binary", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write([]byte{0x00, 0x01})
	})
	mux.HandleFunc("/big", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(strings.Repeat("x", 5000)))
	})
	mux.HandleFunc("/json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	_, err := Fetch(context.Background(), server.URL+"/binary", Policy{AllowPrivate: true})
	if ErrorCode(err) != CodeUnsupportedContent {
		t.Fatalf("binary content type error = %v", err)
	}
	result, err := Fetch(context.Background(), server.URL+"/big", Policy{AllowPrivate: true, MaxResponseBytes: 1000})
	if err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}
	if !result.Truncated || len(result.Body) != 1000 {
		t.Fatalf("truncation = %v len=%d", result.Truncated, len(result.Body))
	}
	result, err = Fetch(context.Background(), server.URL+"/json", Policy{AllowPrivate: true})
	if err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}
	if result.Body != `{"ok":true}` {
		t.Fatalf("json body = %q", result.Body)
	}
	if !strings.HasPrefix(result.URL, server.URL) {
		t.Fatalf("result URL = %q", result.URL)
	}
}

func TestFetchPropagatesContextCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("slow"))
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Fetch(ctx, server.URL, Policy{AllowPrivate: true})
	if err == nil {
		t.Fatal("cancelled fetch succeeded")
	}
	if !errors.Is(err, context.Canceled) && ErrorCode(err) != CodeProviderError {
		t.Fatalf("cancellation error = %v", err)
	}
}
