package netguard

import (
	"net/netip"
	"testing"
)

func TestRefused(t *testing.T) {
	for _, tc := range []struct {
		addr string
		want bool
	}{
		{"8.8.8.8", false}, {"2606:4700::1111", false}, {"100.63.255.255", false}, {"198.20.0.1", false},
		{"127.0.0.1", true}, {"::1", true}, {"10.1.2.3", true}, {"192.168.100.1", true}, {"172.16.0.1", true},
		{"169.254.169.254", true}, {"fe80::1", true}, {"fc00::1", true}, {"fd12::1", true},
		{"0.0.0.0", true}, {"0.1.2.3", true}, {"::", true}, {"224.0.0.1", true}, {"ff02::1", true},
		{"100.64.0.1", true}, {"100.127.255.255", true}, {"198.18.0.1", true}, {"198.19.255.255", true},
		{"240.0.0.1", true}, {"255.255.255.255", true}, {"192.0.0.8", true},
		{"192.0.2.1", true}, {"198.51.100.7", true}, {"203.0.113.9", true}, {"2001:db8::1", true},
		{"64:ff9b::a00:1", true}, {"64:ff9b::808:808", true}, {"64:ff9b:1::1", true}, {"2002::1", true},
		{"::ffff:10.0.0.1", true}, {"::ffff:8.8.8.8", false},
		{"2002:c0a8:7a::1%eth0", true}, {"64:ff9b::c0a8:7a%eth0", true}, {"2001:db8::1%eth0", true},
		{"2606:4700::1111%eth0", true}, {"8.8.8.8", false},
		{"2001:0:4136:e378:8000:63bf:3fff:fdd2", true}, {"2001:1ff::1", true}, {"2001:200::1", false},
		{"::808:808", true}, {"fec0::1", true}, {"feff::1", true},
	} {
		if got := Refused(netip.MustParseAddr(tc.addr)); got != tc.want {
			t.Errorf("Refused(%s) = %v, want %v", tc.addr, got, tc.want)
		}
	}
}
