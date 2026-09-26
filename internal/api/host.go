package api

import (
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// defaultPort is the agent's documented listen port, used when the incoming
// request does not carry one (for example when it arrives through a proxy).
const defaultPort = 8787

// forwardedValue reads a proxy header, keeping only the first hop value.
func forwardedValue(r *http.Request, header string) string {
	value := r.Header.Get(header)
	if value == "" {
		return ""
	}
	if index := strings.IndexByte(value, ','); index >= 0 {
		value = value[:index]
	}
	return strings.TrimSpace(value)
}

// requestAuthority reports the scheme and host:port the client used to reach us.
func requestAuthority(r *http.Request) (string, string) {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if forwarded := forwardedValue(r, "X-Forwarded-Proto"); forwarded != "" {
		scheme = forwarded
	}
	host := r.Host
	if forwarded := forwardedValue(r, "X-Forwarded-Host"); forwarded != "" {
		host = forwarded
	}
	return scheme, host
}

// requestPort reports the port the client used, falling back to defaultPort.
func requestPort(r *http.Request) string {
	_, host := requestAuthority(r)
	if _, port, err := net.SplitHostPort(host); err == nil && port != "" {
		return port
	}
	if port := forwardedValue(r, "X-Forwarded-Port"); port != "" {
		return port
	}
	return strconv.Itoa(defaultPort)
}

// hostCandidates lists LAN authorities this agent can be reached at, so the
// dashboard can offer a usable address when the page was opened over localhost.
func hostCandidates(r *http.Request) []string {
	// Opened from another device: that device already used the right address, and
	// it is the only one we can be sure about (interface addresses inside a
	// bridged container are never reachable from the phone).
	if _, host := requestAuthority(r); host != "" && !hostIsLoopback(r) {
		return []string{host}
	}

	port := requestPort(r)
	out := make([]string, 0, 4)
	seen := map[string]bool{}
	add := func(ip string) {
		if ip == "" {
			return
		}
		value := net.JoinHostPort(ip, port)
		if seen[value] {
			return
		}
		seen[value] = true
		out = append(out, value)
	}

	interfaces, err := net.Interfaces()
	if err != nil {
		return out
	}
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			network, ok := addr.(*net.IPNet)
			if !ok {
				continue
			}
			ip := network.IP
			if ip.IsLoopback() || !ip.IsGlobalUnicast() {
				continue
			}
			if ip4 := ip.To4(); ip4 != nil {
				add(ip4.String())
				continue
			}
			add(ip.String())
		}
	}
	return out
}

// hostIsLoopback reports whether the page was opened over localhost, in which
// case the QR code cannot be scanned by another device as-is.
func hostIsLoopback(r *http.Request) bool {
	_, host := requestAuthority(r)
	name := host
	if parsed, _, err := net.SplitHostPort(host); err == nil {
		name = parsed
	}
	name = strings.Trim(name, "[]")
	if strings.EqualFold(name, "localhost") {
		return true
	}
	if ip := net.ParseIP(name); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// pairingBaseURL builds the base URL encoded into the pairing QR code.
//
// The dashboard may override host/port/scheme so a page opened over localhost
// still yields an address the phone can reach.
func pairingBaseURL(r *http.Request) string {
	scheme, host := requestAuthority(r)
	if override := strings.TrimSpace(r.URL.Query().Get("scheme")); override != "" {
		scheme = override
	}
	if override := strings.TrimSpace(r.URL.Query().Get("host")); override != "" {
		host = override
		// A bare IP keeps this request's port (that is what the dashboard's
		// address picker sends); a hostname is taken verbatim so reverse-proxy
		// names such as `nas.example.com` are not rewritten with :8787.
		if _, _, err := net.SplitHostPort(host); err != nil && net.ParseIP(host) != nil {
			port := strings.TrimSpace(r.URL.Query().Get("port"))
			if port == "" {
				port = requestPort(r)
			}
			if !(port == "80" && scheme == "http") && !(port == "443" && scheme == "https") {
				host = net.JoinHostPort(host, port)
			}
		}
	}
	return scheme + "://" + host + "/"
}

// clientKey identifies a caller for lightweight rate limiting.
func clientKey(r *http.Request) string {
	if forwarded := forwardedValue(r, "X-Forwarded-For"); forwarded != "" {
		return forwarded
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

// rateLimiter allows one action per key per interval.
type rateLimiter struct {
	mu       sync.Mutex
	interval time.Duration
	last     map[string]time.Time
}

func newRateLimiter(interval time.Duration) *rateLimiter {
	return &rateLimiter{interval: interval, last: map[string]time.Time{}}
}

func (l *rateLimiter) allow(key string) bool {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	if last, ok := l.last[key]; ok && now.Sub(last) < l.interval {
		return false
	}
	l.last[key] = now
	// Keep the map bounded; entries older than the interval carry no state.
	if len(l.last) > 128 {
		for entry, seen := range l.last {
			if now.Sub(seen) >= l.interval {
				delete(l.last, entry)
			}
		}
	}
	return true
}
