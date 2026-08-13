package llm

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"os"
	"strings"
)

// ValidateBaseURL enforces the provider network boundary before a request is made.
func ValidateBaseURL(raw string, mode Mode) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Scheme == "" {
		return nil, ErrInvalidURL
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("%w: credentials, query, and fragment are forbidden", ErrInvalidURL)
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if host == "" {
		return nil, ErrInvalidURL
	}
	switch mode {
	case ModeCloud:
		if u.Scheme != "https" {
			return nil, fmt.Errorf("%w: cloud providers require HTTPS", ErrInvalidURL)
		}
		if ip, err := netip.ParseAddr(host); err == nil && !isPublicIP(ip) {
			return nil, ErrUnsafeAddress
		}
	case ModeLocal:
		if u.Scheme != "http" && u.Scheme != "https" {
			return nil, ErrInvalidURL
		}
		if host != "localhost" {
			ip, err := netip.ParseAddr(host)
			if err != nil || !ip.IsLoopback() {
				return nil, fmt.Errorf("%w: local providers must use a loopback host", ErrUnsafeAddress)
			}
		}
	default:
		return nil, fmt.Errorf("%w: unknown mode", ErrInvalidURL)
	}
	if u.Path == "" {
		u.Path = "/"
	}
	return u, nil
}

func isPublicIP(ip netip.Addr) bool {
	return ip.IsValid() && !ip.IsLoopback() && !ip.IsPrivate() && !ip.IsLinkLocalUnicast() && !ip.IsLinkLocalMulticast() && !ip.IsMulticast() && !ip.IsUnspecified()
}

type ipResolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

func environmentProxyURL() *url.URL {
	// Read env directly so tests can override with t.Setenv. net/http caches
	// ProxyFromEnvironment on first use, which would hide later changes.
	for _, key := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy"} {
		raw := strings.TrimSpace(os.Getenv(key))
		if raw == "" {
			continue
		}
		if !strings.Contains(raw, "://") {
			raw = "http://" + raw
		}
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" {
			continue
		}
		return u
	}
	return nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip, err := netip.ParseAddr(host)
	return err == nil && ip.IsLoopback()
}

func effectiveProxyPort(u *url.URL) string {
	if u.Port() != "" {
		return u.Port()
	}
	if strings.EqualFold(u.Scheme, "https") {
		return "443"
	}
	return "80"
}

// dialIsEnvironmentProxy reports whether host:port is the process HTTP(S) proxy.
// Cloud requests may dial a user-configured proxy (Clash, system proxy) even when
// that address is loopback or private; the provider URL is still validated separately.
func dialIsEnvironmentProxy(host, port string) bool {
	proxy := environmentProxyURL()
	if proxy == nil {
		return false
	}
	if port != effectiveProxyPort(proxy) {
		return false
	}
	ph := proxy.Hostname()
	if strings.EqualFold(host, ph) {
		return true
	}
	return isLoopbackHost(host) && isLoopbackHost(ph)
}

func cloudDialAllowed(host, port string, ip netip.Addr) bool {
	if isPublicIP(ip) {
		return true
	}
	return dialIsEnvironmentProxy(host, port) && (ip.IsLoopback() || ip.IsPrivate())
}

func validatingDialer(mode Mode, resolver ipResolver) func(context.Context, string, string) (net.Conn, error) {
	dialer := &net.Dialer{}
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, ErrUnsafeAddress
		}
		ips, err := resolver.LookupNetIP(ctx, "ip", host)
		if err != nil || len(ips) == 0 {
			return nil, fmt.Errorf("resolve provider: %w", err)
		}
		for _, ip := range ips {
			allowed := ip.IsLoopback()
			if mode == ModeCloud {
				allowed = cloudDialAllowed(host, port, ip)
			}
			if !allowed {
				return nil, fmt.Errorf("%w: %s", ErrUnsafeAddress, ip)
			}
		}
		var errs []error
		for _, ip := range ips {
			conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			if err == nil {
				return conn, nil
			}
			errs = append(errs, err)
		}
		return nil, errors.Join(errs...)
	}
}
