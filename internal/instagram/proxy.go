package instagram

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/proxy"
)

// ParseProxy validates a per-account proxy URL. Supported forms:
//
//	http://host:port, https://host:port, socks5://host:port, socks5h://host:port
//	(each optionally with user:pass@), plus the provider shorthand
//	host:port and host:port:user:pass (treated as HTTP proxies).
//
// An empty string means "direct connection" and returns (nil, nil).
func ParseProxy(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	if !strings.Contains(raw, "://") {
		raw = fromShorthand(raw)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("geçersiz proxy adresi: %w", err)
	}
	switch u.Scheme {
	case "http", "https", "socks5", "socks5h":
	default:
		return nil, fmt.Errorf("desteklenmeyen proxy türü %q (http, https, socks5, socks5h kullanın)", u.Scheme)
	}
	if u.Hostname() == "" || u.Port() == "" {
		return nil, fmt.Errorf("proxy adresinde host ve port gerekli")
	}
	return u, nil
}

func fromShorthand(raw string) string {
	parts := strings.Split(raw, ":")
	if len(parts) == 4 {
		u := url.URL{Scheme: "http", Host: parts[0] + ":" + parts[1], User: url.UserPassword(parts[2], parts[3])}
		return u.String()
	}
	return "http://" + raw
}

// MaskProxy renders a proxy URL for display with its password hidden.
func MaskProxy(u *url.URL) string {
	if u == nil {
		return ""
	}
	if u.User == nil {
		return fmt.Sprintf("%s://%s", u.Scheme, u.Host)
	}
	if _, hasPass := u.User.Password(); hasPass {
		return fmt.Sprintf("%s://%s:***@%s", u.Scheme, u.User.Username(), u.Host)
	}
	return fmt.Sprintf("%s://%s@%s", u.Scheme, u.User.Username(), u.Host)
}

// newHTTPClient builds an HTTP client whose every connection goes through
// proxyURL (or directly when nil). Environment proxy variables are ignored
// on purpose so an account never silently leaks out through another route.
func newHTTPClient(proxyURL *url.URL) (*http.Client, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil

	if proxyURL != nil {
		switch proxyURL.Scheme {
		case "http", "https":
			transport.Proxy = http.ProxyURL(proxyURL)
		case "socks5", "socks5h":
			dialer, err := proxy.FromURL(proxyURL, &net.Dialer{Timeout: 15 * time.Second})
			if err != nil {
				return nil, fmt.Errorf("socks5 proxy hazırlanamadı: %w", err)
			}
			if cd, ok := dialer.(proxy.ContextDialer); ok {
				transport.DialContext = cd.DialContext
			} else {
				transport.DialContext = func(_ context.Context, network, addr string) (net.Conn, error) {
					return dialer.Dial(network, addr)
				}
			}
		}
	}

	return &http.Client{Transport: transport, Timeout: 60 * time.Second}, nil
}
