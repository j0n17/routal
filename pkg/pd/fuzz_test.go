package pd

import (
	"net"
	"testing"
	"time"

	"github.com/insomniacslk/dhcp/dhcpv6"
	"github.com/insomniacslk/dhcp/iana"
)

// FuzzParse feeds arbitrary DHCPv6 answers, as a hostile or broken ISP would send them: a lease
// that is accepted has a usable prefix and timers that cannot make the client renew in a loop,
// and the Renew built from it is a valid message.
func FuzzParse(f *testing.F) {
	duid := DUID(net.HardwareAddr{2, 0, 0, 0, 0, 1})
	iaid := IAID("wan0")

	sol, err := Message(dhcpv6.MessageTypeSolicit, duid, iaid, 56, nil)
	if err != nil {
		f.Fatal(err)
	}

	f.Add(sol.ToBytes())

	// answers of a server: a normal one, timers of 0, a deprecated prefix, a refusal, no server id
	server := &dhcpv6.DUIDLL{HWType: iana.HWTypeEthernet, LinkLayerAddr: net.HardwareAddr{2, 0, 0, 0, 0, 2}}

	for _, c := range []struct {
		t1, t2, preferred, valid time.Duration
		status                   iana.StatusCode
		server                   bool
	}{
		{300 * time.Second, 480 * time.Second, 600 * time.Second, 1200 * time.Second, iana.StatusSuccess, true},
		{0, 0, 600 * time.Second, 1200 * time.Second, iana.StatusSuccess, true},
		{time.Second, time.Second, 0, 1200 * time.Second, iana.StatusSuccess, true},
		{0, 0, 0, 0, iana.StatusNoBinding, true},
		{300 * time.Second, 480 * time.Second, 600 * time.Second, 1200 * time.Second, iana.StatusSuccess, false},
	} {
		reply, _ := dhcpv6.NewMessage()
		reply.MessageType = dhcpv6.MessageTypeReply
		reply.AddOption(dhcpv6.OptClientID(duid))

		if c.server {
			reply.AddOption(dhcpv6.OptServerID(server))
		}

		iapd := &dhcpv6.OptIAPD{IaId: iaid, T1: c.t1, T2: c.t2}
		iapd.Options.Add(&dhcpv6.OptIAPrefix{
			PreferredLifetime: c.preferred, ValidLifetime: c.valid,
			Prefix: &net.IPNet{IP: net.ParseIP("2001:db8:aa00::"), Mask: net.CIDRMask(56, 128)},
		})

		if c.status != iana.StatusSuccess {
			iapd.Options.Add(&dhcpv6.OptStatusCode{StatusCode: c.status, StatusMessage: "no binding"})
		}

		reply.AddOption(iapd)
		f.Add(reply.ToBytes())
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		m, err := dhcpv6.MessageFromBytes(data)
		if err != nil {
			return
		}

		l, err := Parse(m, iaid, time.Unix(1700000000, 0))
		if err != nil {
			if l != nil {
				t.Fatalf("lease returned with error %v", err)
			}

			return
		}

		switch {
		case !l.Prefix.IsValid() || !l.Prefix.Addr().Is6() || l.Prefix.Bits() > 64:
			t.Fatalf("unusable prefix %v", l.Prefix)
		case l.Prefix != l.Prefix.Masked():
			t.Fatalf("prefix %v has host bits", l.Prefix)
		case l.Valid == 0:
			t.Fatal("lease with a valid lifetime of 0")
		case l.T1 < MinRenew || l.T2 < l.T1:
			t.Fatalf("timers T1=%v T2=%v", l.T1, l.T2)
		case len(l.ServerID) == 0:
			t.Fatal("no server identifier")
		}

		if _, _, err := Subnet(l.Prefix, 0); err != nil {
			t.Fatalf("subnet 0 of %v: %v", l.Prefix, err)
		}

		for _, typ := range []dhcpv6.MessageType{dhcpv6.MessageTypeRenew, dhcpv6.MessageTypeRebind, dhcpv6.MessageTypeRelease} {
			out, err := Message(typ, duid, iaid, 56, l)
			if err != nil {
				t.Fatalf("%v from the lease: %v", typ, err)
			}

			if _, err := dhcpv6.MessageFromBytes(out.ToBytes()); err != nil {
				t.Fatalf("%v from the lease does not parse: %v", typ, err)
			}
		}
	})
}
