package web

import (
	"context"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// clientInfo is who sent a request and whether it arrived over HTTPS.
type clientInfo struct {
	IP    netip.Addr
	HTTPS bool
}

type clientKey struct{}

// resolveClient trusts X-Forwarded-For and X-Forwarded-Proto only when the
// direct peer is a configured trusted proxy.
func resolveClient(r *http.Request, trusted []netip.Prefix) clientInfo {
	info := clientInfo{IP: peerAddr(r.RemoteAddr), HTTPS: r.TLS != nil}
	if !inPrefixes(info.IP, trusted) {
		return info
	}
	var hops []string
	for _, v := range r.Header.Values("X-Forwarded-For") {
		hops = append(hops, strings.Split(v, ",")...)
	}
	// Walk from the nearest hop and stop at the first address we don't trust.
	for i := len(hops) - 1; i >= 0; i-- {
		a, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil {
			break
		}
		info.IP = a.Unmap()
		if !inPrefixes(info.IP, trusted) {
			break
		}
	}
	if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
		first, _, _ := strings.Cut(proto, ",")
		info.HTTPS = strings.EqualFold(strings.TrimSpace(first), "https")
	}
	return info
}

func peerAddr(remote string) netip.Addr {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		host = remote
	}
	a, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}
	}
	return a.Unmap()
}

func inPrefixes(a netip.Addr, prefixes []netip.Prefix) bool {
	for _, p := range prefixes {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

func withClient(trusted []netip.Prefix, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), clientKey{}, resolveClient(r, trusted))
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func clientFrom(ctx context.Context) clientInfo {
	info, _ := ctx.Value(clientKey{}).(clientInfo)
	return info
}
