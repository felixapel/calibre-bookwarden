// Package netguard decides which addresses outbound clients may dial. Both
// the source client and the agent seats refuse everything that isn't a
// public unicast address.
package netguard

import (
	"fmt"
	"net/netip"
	"slices"
	"syscall"
)

// special are the special-purpose ranges the net/netip predicates don't
// cover: this network, CGNAT (Tailscale uses it),
// IETF protocol assignments including Teredo (2001::/23), documentation,
// benchmarking, reserved, IPv4-compatible (::/96), site-local, NAT64 and 6to4.
var special = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"), netip.MustParsePrefix("198.51.100.0/24"), netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("2001::/23"), netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("::/96"), netip.MustParsePrefix("fec0::/10"),
	netip.MustParsePrefix("64:ff9b::/96"), netip.MustParsePrefix("64:ff9b:1::/48"), netip.MustParsePrefix("2002::/16"),
}

// Refused reports whether a is loopback, private (RFC 1918, ULA), link-local,
// multicast, unspecified or in a special-purpose range. NAT64 and 6to4
// prefixes are refused whole, so an embedded private IPv4 address can't get
// through them. An address with an IPv6 zone is always refused: Prefix.Contains
// never matches a zoned address, so the range checks would miss it.
func Refused(a netip.Addr) bool {
	if a.Zone() != "" {
		return true
	}
	a = a.Unmap()
	return a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() ||
		a.IsMulticast() || a.IsUnspecified() || a.IsInterfaceLocalMulticast() ||
		slices.ContainsFunc(special, func(p netip.Prefix) bool { return p.Contains(a) })
}

// DialControl returns a net.Dialer Control function, which sees the resolved
// address of every connection, so a name that resolves somewhere forbidden is
// refused. With allow, every address dialled must be inside it; without, the
// addresses Refused names are refused unless allowPrivate. A refusal wraps
// refused.
func DialControl(allow []netip.Prefix, allowPrivate bool, refused error) func(network, address string, c syscall.RawConn) error {
	return func(_, address string, _ syscall.RawConn) error {
		ap, err := netip.ParseAddrPort(address)
		if err != nil {
			return fmt.Errorf("%w: %s", refused, address)
		}
		a := ap.Addr().Unmap()
		switch {
		case len(allow) > 0:
			if !slices.ContainsFunc(allow, func(p netip.Prefix) bool { return p.Contains(a) }) {
				return fmt.Errorf("%w: %s", refused, address)
			}
		case !allowPrivate && Refused(a):
			return fmt.Errorf("%w: %s", refused, address)
		}
		return nil
	}
}
