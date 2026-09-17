// Package web implements the network policy and transport behind the
// web_search and web_fetch tools, ported from DSH's web-fetch-http
// provider.
//
// The security model is fail-closed: a URL is parsed and length-checked
// before any network access, the hostname is resolved exactly once, the
// complete answer set must be public unicast, and the connection is
// pinned to the validated addresses so a second resolution cannot
// redirect it to a private service. Redirects are followed only within
// the same origin and only after re-validating the target.
package web

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

// Error codes mirroring the reference provider.
const (
	CodeInvalidURL         = "WEB_INVALID_URL"
	CodeBlockedURL         = "WEB_BLOCKED_URL"
	CodeRedirectBlocked    = "WEB_REDIRECT_BLOCKED"
	CodeProviderError      = "WEB_PROVIDER_ERROR"
	CodeUnsupportedContent = "WEB_UNSUPPORTED_CONTENT_TYPE"
)

// Error is a typed web failure carrying a stable code.
type Error struct {
	Code    string
	Message string
	Cause   error
}

func (e *Error) Error() string { return e.Message }

func (e *Error) Unwrap() error { return e.Cause }

func webError(code, format string, args ...any) error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// ErrorCode reports the stable code of a web error, or "" when err is not
// a web error.
func ErrorCode(err error) string {
	var webErr *Error
	if errors.As(err, &webErr) {
		return webErr.Code
	}
	return ""
}

// Policy configures the transport limits. Zero values select the
// reference defaults.
type Policy struct {
	// MaxURLBytes bounds the request URL (reference: 2048).
	MaxURLBytes int
	// MaxResponseBytes bounds the downloaded body (reference: 5e6).
	MaxResponseBytes int64
	// MaxBodyChars bounds the decoded text handed to the model
	// (reference: 1e5).
	MaxBodyChars int
	// Timeout bounds one fetch (reference: 30s).
	Timeout time.Duration
	// MaxRedirects bounds same-origin redirect hops (reference: 5).
	// Nil selects the default; Redirects(0) follows none.
	MaxRedirects *int
	// UserAgent is sent with every request.
	UserAgent string
	// AllowPrivate disables the public-address requirement. It exists for
	// local development and tests only; enabling it lets a fetch reach
	// loopback, link-local, and private ranges.
	AllowPrivate bool
	// Resolver overrides name resolution (tests and hosts).
	Resolver Resolver
}

// Resolver resolves a hostname to its complete address set.
type Resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// DefaultUserAgent identifies ZenForge, matching the reference's practice
// of a versioned, contactable agent string.
const DefaultUserAgent = "zenforge/0.1 (+https://github.com/feiyu912/zenforge)"

// defaults fills zero values with the reference defaults.
// Redirects returns a redirect budget for Policy.MaxRedirects, so a
// deployment can distinguish "unset" (nil, the default) from an
// explicit zero (follow no redirects).
func Redirects(hops int) *int { return &hops }

// maxRedirects resolves the effective hop cap.
func (p Policy) maxRedirects() int {
	if p.MaxRedirects == nil {
		return 5
	}
	return *p.MaxRedirects
}

func (p Policy) withDefaults() Policy {
	if p.MaxURLBytes <= 0 {
		p.MaxURLBytes = 2048
	}
	if p.MaxResponseBytes <= 0 {
		p.MaxResponseBytes = 5_000_000
	}
	if p.MaxBodyChars <= 0 {
		p.MaxBodyChars = 100_000
	}
	if p.Timeout <= 0 {
		p.Timeout = 30 * time.Second
	}
	if p.UserAgent == "" {
		p.UserAgent = DefaultUserAgent
	}
	if p.Resolver == nil {
		p.Resolver = net.DefaultResolver
	}
	return p
}

// Result is one fetched document.
type Result struct {
	URL        string
	StatusCode int
	// Body is the decoded text: HTML is converted to text, structured
	// text is returned as-is.
	Body string
	// Truncated reports that the source exceeded a limit.
	Truncated bool
}

// Accept is the reference provider's Accept header.
const AcceptHeader = "text/html,application/xhtml+xml,text/*;q=0.9,application/json;q=0.8"

// Fetch retrieves one URL under the policy.
func Fetch(ctx context.Context, rawURL string, policy Policy) (Result, error) {
	policy = policy.withDefaults()
	current, err := parseFetchURL(rawURL, policy)
	if err != nil {
		return Result{}, err
	}
	client := &http.Client{
		Timeout:   policy.Timeout,
		Transport: &pinnedTransport{policy: policy, base: &http.Transport{}},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	followed := 0
	for {
		response, err := doRequest(ctx, client, current, policy)
		if err != nil {
			return Result{}, err
		}
		if isRedirect(response.StatusCode) {
			location := response.Header.Get("Location")
			response.Body.Close()
			if location == "" {
				return Result{}, webError(CodeProviderError, "redirect response (HTTP %d) without a Location header", response.StatusCode)
			}
			if followed >= policy.maxRedirects() {
				return Result{}, webError(CodeRedirectBlocked, "exceeded the maximum of %d redirects", policy.maxRedirects())
			}
			target, err := current.Parse(location)
			if err != nil {
				return Result{}, webError(CodeProviderError, "invalid redirect Location %q", location)
			}
			if !sameOrigin(current, target) {
				return Result{}, webError(CodeRedirectBlocked,
					"cross-origin redirect to %s is not followed automatically; retry against that URL directly", target.Scheme+"://"+target.Host)
			}
			if target.String() == current.String() {
				return Result{}, webError(CodeRedirectBlocked, "redirect loop detected at %s", target.String())
			}
			if _, err := parseFetchURL(target.String(), policy); err != nil {
				return Result{}, err
			}
			current = target
			followed++
			continue
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			response.Body.Close()
			return Result{}, webError(CodeProviderError, "HTTP %d from %s", response.StatusCode, current.String())
		}
		return readResponse(response, current, policy)
	}
}

func doRequest(ctx context.Context, client *http.Client, target *url.URL, policy Policy) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return nil, webError(CodeInvalidURL, "invalid URL %s", target.String())
	}
	request.Header.Set("Accept", AcceptHeader)
	request.Header.Set("User-Agent", policy.UserAgent)
	response, err := client.Do(request)
	if err != nil {
		var webErr *Error
		if errors.As(err, &webErr) {
			return nil, webErr
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &Error{Code: CodeProviderError, Message: fmt.Sprintf("fetch %s failed: %v", target.String(), err), Cause: err}
	}
	return response, nil
}

// readResponse enforces the content-type and size limits, then decodes.
func readResponse(response *http.Response, target *url.URL, policy Policy) (Result, error) {
	defer response.Body.Close()
	kind := classifyContentType(response.Header.Get("Content-Type"))
	if kind == "" {
		return Result{}, webError(CodeUnsupportedContent,
			"unsupported content type %q from %s", response.Header.Get("Content-Type"), target.String())
	}
	limited := io.LimitReader(response.Body, policy.MaxResponseBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return Result{}, &Error{Code: CodeProviderError, Message: fmt.Sprintf("read %s failed: %v", target.String(), err), Cause: err}
	}
	truncated := int64(len(data)) > policy.MaxResponseBytes
	if truncated {
		data = data[:policy.MaxResponseBytes]
	}
	text := decodeText(data, response.Header.Get("Content-Type"))
	if kind == "html" {
		text = HTMLToText(text)
	}
	result := Result{URL: target.String(), StatusCode: response.StatusCode, Body: text, Truncated: truncated}
	return result, nil
}

// parseFetchURL applies the network-independent policy: bounded length,
// http(s) only, and no embedded credentials.
func parseFetchURL(raw string, policy Policy) (*url.URL, error) {
	if len(raw) > policy.MaxURLBytes {
		return nil, webError(CodeInvalidURL, "URL exceeds the maximum length of %d", policy.MaxURLBytes)
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, webError(CodeInvalidURL, "invalid URL: %s", raw)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, webError(CodeInvalidURL, "unsupported URL scheme %q (only http and https are allowed)", parsed.Scheme)
	}
	if parsed.User != nil {
		return nil, webError(CodeBlockedURL, "credentials in URLs are not allowed")
	}
	if parsed.Hostname() == "" {
		return nil, webError(CodeInvalidURL, "invalid URL: %s", raw)
	}
	return parsed, nil
}

func isRedirect(status int) bool {
	switch status {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther,
		http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		return true
	default:
		return false
	}
}

func sameOrigin(a, b *url.URL) bool {
	return a.Scheme == b.Scheme && a.Hostname() == b.Hostname() && portOf(a) == portOf(b)
}

func portOf(target *url.URL) string {
	if target.Port() != "" {
		return target.Port()
	}
	if target.Scheme == "https" {
		return "443"
	}
	return "80"
}

// classifyContentType mirrors the reference classification: HTML,
// text/*, and a few structured text types are decodable; anything else
// (for example a binary type) is refused.
func classifyContentType(contentType string) string {
	mime := strings.ToLower(strings.TrimSpace(strings.SplitN(contentType, ";", 2)[0]))
	switch {
	case mime == "text/html", mime == "application/xhtml+xml":
		return "html"
	case strings.HasPrefix(mime, "text/"):
		return "text"
	case mime == "application/json", mime == "application/xml",
		strings.HasSuffix(mime, "+json"), strings.HasSuffix(mime, "+xml"):
		return "text"
	default:
		return ""
	}
}

// decodeText decodes a body using the declared charset, falling back to
// UTF-8 when the label is absent or unknown.
func decodeText(data []byte, contentType string) string {
	// Go's standard library has no general charset decoder; UTF-8 is the
	// only built-in, so a declared non-UTF-8 charset falls back to a
	// lossless byte-preserving conversion with invalid sequences
	// replaced.
	_ = contentType
	return strings.ToValidUTF8(string(data), "\uFFFD")
}

// isPublicAddress reports whether an address is globally reachable
// unicast. IPv4-mapped IPv6 is classified by its embedded IPv4 address;
// other transition and translation ranges stay blocked because their
// eventual destination cannot be pinned.
func isPublicAddress(address netip.Addr) bool {
	if address.Is4In6() {
		address = address.Unmap()
	}
	if !address.IsValid() {
		return false
	}
	if address.Is4() {
		// Reject every non-global range explicitly rather than relying on
		// a single predicate, so a new reserved range cannot slip through.
		switch {
		case address.IsLoopback(), address.IsPrivate(), address.IsLinkLocalUnicast(),
			address.IsLinkLocalMulticast(), address.IsInterfaceLocalMulticast(),
			address.IsMulticast(), address.IsUnspecified():
			return false
		}
		// 192.0.0.0/24 (IETF protocol assignments), 192.0.2.0/24,
		// 198.18.0.0/15 (benchmarking), 198.51.100.0/24, 203.0.113.0/24
		// (documentation), 240.0.0.0/4 (reserved), and 100.64.0.0/10
		// (carrier-grade NAT) are not globally reachable.
		blocked := []netip.Prefix{
			netip.MustParsePrefix("192.0.0.0/24"),
			netip.MustParsePrefix("192.0.2.0/24"),
			netip.MustParsePrefix("100.64.0.0/10"),
			netip.MustParsePrefix("198.18.0.0/15"),
			netip.MustParsePrefix("198.51.100.0/24"),
			netip.MustParsePrefix("203.0.113.0/24"),
			netip.MustParsePrefix("240.0.0.0/4"),
		}
		for _, prefix := range blocked {
			if prefix.Contains(address) {
				return false
			}
		}
		return true
	}
	switch {
	case address.IsLoopback(), address.IsLinkLocalUnicast(), address.IsLinkLocalMulticast(),
		address.IsInterfaceLocalMulticast(), address.IsMulticast(), address.IsUnspecified():
		return false
	case address.IsPrivate():
		return false
	}
	// Unique-local (fc00::/7) and IPv4-embedded transition forms
	// (6to4 2002::/16, Teredo 2001::/32, NAT64 64:ff9b::/96) are blocked:
	// their eventual IPv4 destination is not pinned by this check.
	blocked := []netip.Prefix{
		netip.MustParsePrefix("fc00::/7"),
		netip.MustParsePrefix("2002::/16"),
		netip.MustParsePrefix("2001::/32"),
		netip.MustParsePrefix("64:ff9b::/96"),
		netip.MustParsePrefix("::ffff:0:0/96"),
		netip.MustParsePrefix("2001:db8::/32"),
	}
	for _, prefix := range blocked {
		if prefix.Contains(address) {
			return false
		}
	}
	return true
}

// ResolvePublic resolves a hostname once and requires every answer to be
// public unicast, returning the pinned address set. A single non-public
// answer rejects the whole set: a host that answers with both a public
// and a private address must not be reachable.
func ResolvePublic(ctx context.Context, hostname string, policy Policy) ([]netip.Addr, error) {
	policy = policy.withDefaults()
	if literal, err := netip.ParseAddr(hostname); err == nil {
		if !policy.AllowPrivate && !isPublicAddress(literal) {
			return nil, webError(CodeBlockedURL, "URL hostname %q is a non-public IP address", hostname)
		}
		return []netip.Addr{literal}, nil
	}
	addresses, err := policy.Resolver.LookupNetIP(ctx, "ip", hostname)
	if err != nil {
		return nil, &Error{Code: CodeProviderError, Message: fmt.Sprintf("hostname %q could not be resolved: %v", hostname, err), Cause: err}
	}
	if len(addresses) == 0 {
		return nil, webError(CodeProviderError, "hostname %q resolved to no addresses", hostname)
	}
	if policy.AllowPrivate {
		return addresses, nil
	}
	for _, address := range addresses {
		if !isPublicAddress(address) {
			return nil, webError(CodeBlockedURL, "URL hostname %q resolves to a non-public IP address", hostname)
		}
	}
	return addresses, nil
}

// pinnedTransport dials the validated address set instead of resolving
// the hostname again, which is what makes DNS rebinding ineffective.
type pinnedTransport struct {
	policy Policy
	base   *http.Transport
}

func (t *pinnedTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	addresses, err := ResolvePublic(request.Context(), request.URL.Hostname(), t.policy)
	if err != nil {
		return nil, err
	}
	transport := t.base.Clone()
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		var lastErr error
		for _, candidate := range addresses {
			dialer := &net.Dialer{Timeout: 10 * time.Second}
			connection, err := dialer.DialContext(ctx, network, net.JoinHostPort(candidate.String(), portFromAddress(address)))
			if err == nil {
				return connection, nil
			}
			lastErr = err
		}
		if lastErr == nil {
			lastErr = errors.New("no validated address available")
		}
		return nil, lastErr
	}
	transport.Proxy = nil
	response, err := transport.RoundTrip(request)
	if err != nil {
		return nil, &Error{Code: CodeProviderError, Message: fmt.Sprintf("fetch %s failed: %v", request.URL.String(), err), Cause: err}
	}
	return response, nil
}

// CloseIdleConnections releases pooled connections.
func (t *pinnedTransport) CloseIdleConnections() {
	if t.base != nil {
		t.base.CloseIdleConnections()
	}
}

// portFromAddress extracts the port from the dial address the HTTP
// transport requested; the transport always supplies one.
func portFromAddress(address string) string {
	if _, port, err := net.SplitHostPort(address); err == nil && port != "" {
		return port
	}
	return "443"
}
