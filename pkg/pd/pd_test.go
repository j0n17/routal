package pd

import (
	"errors"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/insomniacslk/dhcp/dhcpv6"
	"github.com/insomniacslk/dhcp/iana"
)

func TestSubnet(t *testing.T) {
	for _, c := range []struct {
		delegated string
		id        uint32
		subnet    string
		addr      string
		err       bool
	}{
		{"2001:db8:a00::/56", 0, "2001:db8:a00::/64", "2001:db8:a00::1", false},
		{"2001:db8:a00::/56", 1, "2001:db8:a00:1::/64", "2001:db8:a00:1::1", false},
		{"2001:db8:a00::/56", 255, "2001:db8:a00:ff::/64", "2001:db8:a00:ff::1", false},
		{"2001:db8:a00::/56", 256, "", "", true},
		{"2001:db8:a00:1200::/64", 0, "2001:db8:a00:1200::/64", "2001:db8:a00:1200::1", false},
		{"2001:db8:a00:1200::/64", 1, "", "", true},
		{"2001:db8::/48", 0x1234, "2001:db8:0:1234::/64", "2001:db8:0:1234::1", false},
	} {
		subnet, addr, err := Subnet(netip.MustParsePrefix(c.delegated), c.id)
		if c.err {
			if err == nil {
				t.Errorf("%s id %d: no error", c.delegated, c.id)
			}

			continue
		}

		if err != nil || subnet.String() != c.subnet || addr.String() != c.addr {
			t.Errorf("%s id %d = %s %s %v, want %s %s", c.delegated, c.id, subnet, addr, err, c.subnet, c.addr)
		}
	}
}

// A server answer, built with the same library, round-trips through Parse.
func TestParseAndRenew(t *testing.T) {
	iaid := IAID("isp")
	duid := DUID(net.HardwareAddr{0x52, 0x54, 0, 0, 0, 1})
	server := &dhcpv6.DUIDLL{HWType: iana.HWTypeEthernet, LinkLayerAddr: net.HardwareAddr{2, 0, 0, 0, 0, 1}}

	sol, err := Message(dhcpv6.MessageTypeSolicit, duid, iaid, 56, nil)
	if err != nil {
		t.Fatal(err)
	}

	if hint := sol.Options.OneIAPD().Options.Prefixes()[0].Prefix; hint.String() != "::/56" {
		t.Errorf("hint = %s", hint)
	}

	reply, _ := dhcpv6.NewMessage()
	reply.MessageType = dhcpv6.MessageTypeReply
	reply.AddOption(dhcpv6.OptServerID(server))

	_, n, _ := net.ParseCIDR("2001:db8:a00::/56")
	iapd := &dhcpv6.OptIAPD{IaId: iaid} // T1/T2 0: the client picks them
	iapd.Options.Add(&dhcpv6.OptIAPrefix{Prefix: n, PreferredLifetime: time.Hour, ValidLifetime: 2 * time.Hour})
	reply.AddOption(iapd)

	now := time.Unix(1_800_000_000, 0)

	l, err := Parse(reply, iaid, now)
	if err != nil {
		t.Fatal(err)
	}

	if l.Prefix.String() != "2001:db8:a00::/56" || l.T1 != 30*time.Minute || l.T2 != 48*time.Minute {
		t.Errorf("lease = %+v", l)
	}

	if p, v := l.Remaining(now.Add(90 * time.Minute)); p != 0 || v != 30*time.Minute {
		t.Errorf("remaining = %s %s", p, v)
	}

	renew, err := Message(dhcpv6.MessageTypeRenew, duid, iaid, 0, l)
	if err != nil {
		t.Fatal(err)
	}

	if renew.Options.ServerID() == nil || renew.Options.OneIAPD().Options.Prefixes()[0].Prefix.String() != "2001:db8:a00::/56" {
		t.Errorf("renew does not carry the server and the prefix: %s", renew.Summary())
	}

	rebind, _ := Message(dhcpv6.MessageTypeRebind, duid, iaid, 0, l)
	if rebind.Options.ServerID() != nil {
		t.Error("rebind must not name a server")
	}

	// NoPrefixAvail
	bad, _ := dhcpv6.NewMessage()
	bad.MessageType = dhcpv6.MessageTypeAdvertise
	noPrefix := &dhcpv6.OptIAPD{IaId: iaid}
	noPrefix.Options.Add(&dhcpv6.OptStatusCode{StatusCode: iana.StatusNoPrefixAvail, StatusMessage: "none"})
	bad.AddOption(noPrefix)

	if _, err := Parse(bad, iaid, now); !errors.Is(err, ErrRefused) {
		t.Errorf("NoPrefixAvail: %v, want ErrRefused (the client must solicit again)", err)
	}

	// an answer without a server identifier must not panic
	anon, _ := dhcpv6.NewMessage()
	anon.MessageType = dhcpv6.MessageTypeReply
	anon.AddOption(iapd)

	if _, err := Parse(anon, iaid, now); err == nil {
		t.Error("answer without a server identifier accepted")
	}

	// a deprecated prefix (preferred lifetime 0) must not be renewed in a loop
	dep, _ := dhcpv6.NewMessage()
	dep.MessageType = dhcpv6.MessageTypeReply
	dep.AddOption(dhcpv6.OptServerID(server))

	old := &dhcpv6.OptIAPD{IaId: iaid}
	old.Options.Add(&dhcpv6.OptIAPrefix{Prefix: n, PreferredLifetime: 0, ValidLifetime: time.Hour})
	dep.AddOption(old)

	if l, err := Parse(dep, iaid, now); err != nil || l.T1 < MinRenew || l.T2 < l.T1 {
		t.Errorf("deprecated prefix: %+v %v", l, err)
	}
}

func TestParseRefusals(t *testing.T) {
	iaid := IAID("isp")
	now := time.Unix(1_800_000_000, 0)
	server := &dhcpv6.DUIDLL{HWType: iana.HWTypeEthernet, LinkLayerAddr: net.HardwareAddr{2, 0, 0, 0, 0, 1}}

	reply := func(opts ...dhcpv6.Option) *dhcpv6.Message {
		m, _ := dhcpv6.NewMessage()
		m.MessageType = dhcpv6.MessageTypeReply
		m.AddOption(dhcpv6.OptServerID(server))

		for _, o := range opts {
			m.AddOption(o)
		}

		return m
	}

	// the server no longer knows the lease
	if _, err := Parse(reply(&dhcpv6.OptStatusCode{StatusCode: iana.StatusNoBinding}), iaid, now); !errors.Is(err, ErrRefused) {
		t.Errorf("NoBinding: %v", err)
	}

	// an answer for another IA_PD
	if _, err := Parse(reply(&dhcpv6.OptIAPD{IaId: [4]byte{9, 9, 9, 9}}), iaid, now); err == nil {
		t.Error("answer without our IA_PD accepted")
	}

	// prefixes that cannot be used: withdrawn (valid 0), longer than a /64, IPv4
	iapd := &dhcpv6.OptIAPD{IaId: iaid}
	for _, p := range []struct {
		cidr  string
		valid time.Duration
	}{{"2001:db8:a00::/56", 0}, {"2001:db8:a00::/72", time.Hour}, {"192.0.2.0/24", time.Hour}} {
		_, n, _ := net.ParseCIDR(p.cidr)
		iapd.Options.Add(&dhcpv6.OptIAPrefix{Prefix: n, ValidLifetime: p.valid})
	}

	iapd.Options.Add(&dhcpv6.OptIAPrefix{ValidLifetime: time.Hour}) // no prefix at all

	if _, err := Parse(reply(iapd), iaid, now); !errors.Is(err, ErrRefused) {
		t.Errorf("unusable prefixes: %v", err)
	}
}

// A lease that does not come from Parse (no server options kept) still makes a valid message.
func TestMessageFromBareLease(t *testing.T) {
	duid := DUID(net.HardwareAddr{0x52, 0x54, 0, 0, 0, 1})
	server := &dhcpv6.DUIDLL{HWType: iana.HWTypeEthernet, LinkLayerAddr: net.HardwareAddr{2, 0, 0, 0, 0, 1}}
	l := &Lease{Prefix: netip.MustParsePrefix("2001:db8:a00::/56"), ServerID: server.ToBytes()}

	m, err := Message(dhcpv6.MessageTypeRebind, duid, IAID("isp"), 0, l)
	if err != nil || m.Options.OneIAPD().Options.Prefixes()[0].Prefix.String() != "2001:db8:a00::/56" {
		t.Errorf("rebind: %v %v", m, err)
	}

	l.ServerID = []byte{1}
	if _, err := Message(dhcpv6.MessageTypeRenew, duid, IAID("isp"), 0, l); err == nil {
		t.Error("renew with a broken server id accepted")
	}
}
