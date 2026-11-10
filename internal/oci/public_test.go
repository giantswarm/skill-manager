package oci

import (
	"net/netip"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPublicAddresses(t *testing.T) {
	for addr, want := range map[string]bool{
		"20.50.2.3": true, "2603:1020::1": true,
		"10.0.0.1": false, "100.64.1.2": false, "100.127.255.255": false, "127.0.0.1": false,
		"169.254.169.254": false, "192.0.0.8": false, "198.18.0.1": false, "240.0.0.1": false,
		"0.1.2.3": false, "::1": false, "fd00::1": false, "fe80::1": false, "::ffff:10.0.0.1": false,
		"64:ff9b::a00:1": false, "2001:db8::1": false, "224.0.0.1": false,
	} {
		assert.Equal(t, want, public(netip.MustParseAddr(addr)), addr)
	}
}
