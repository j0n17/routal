// Package pd is the protocol side of DHCPv6 prefix delegation (RFC 8415 IA_PD): messages, lease
// parsing, the /64s handed to LAN links and the renewal timers. cmd/routald drives it on a link.
package pd

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"time"

	"github.com/insomniacslk/dhcp/dhcpv6"
	"github.com/insomniacslk/dhcp/iana"
)

// Lease is a delegated prefix.
type Lease struct {
	Prefix    netip.Prefix  `json:"prefix"`
	Preferred time.Duration `json:"preferred"`
	Valid     time.Duration `json:"valid"`
	T1        time.Duration `json:"t1"`
	T2        time.Duration `json:"t2"`
	Obtained  time.Time     `json:"obtained"`
	ServerID  []byte        `json:"serverID"`

	iapd *dhcpv6.OptIAPD
}

// IAID derives a stable IA identifier from the PrefixDelegationConfig name.
func IAID(name string) [4]byte {
	sum := sha256.Sum256([]byte("routal-pd-" + name))

	return [4]byte(sum[:4])
}

// DUID returns DUID-LL from the link's MAC address, stable even if the state is lost. Links
// without one (PPP) use a DUID-UUID, which the caller persists.
func DUID(mac net.HardwareAddr) dhcpv6.DUID {
	if len(mac) == 6 {
		return &dhcpv6.DUIDLL{HWType: iana.HWTypeEthernet, LinkLayerAddr: mac}
	}

	var u dhcpv6.DUIDUUID

	_, _ = rand.Read(u.UUID[:])

	return &u
}

// ParseDUID reads a persisted DUID.
func ParseDUID(b []byte) (dhcpv6.DUID, error) { return dhcpv6.DUIDFromBytes(b) }

// Message builds a Solicit, Request, Renew or Rebind for one IA_PD. hint asks for a prefix length
// (0: none); lease carries the prefix being requested, renewed or rebound.
func Message(t dhcpv6.MessageType, duid dhcpv6.DUID, iaid [4]byte, hint int, lease *Lease) (*dhcpv6.Message, error) {
	m, err := dhcpv6.NewMessage()
	if err != nil {
		return nil, err
	}

	m.MessageType = t
	m.AddOption(dhcpv6.OptClientID(duid))

	if lease != nil && (t == dhcpv6.MessageTypeRequest || t == dhcpv6.MessageTypeRenew) {
		sid, err := dhcpv6.DUIDFromBytes(lease.ServerID)
		if err != nil {
			return nil, fmt.Errorf("server id: %w", err)
		}

		m.AddOption(dhcpv6.OptServerID(sid))
	}

	m.AddOption(dhcpv6.OptElapsedTime(0))
	m.AddOption(dhcpv6.OptRequestedOption(dhcpv6.OptionDNSRecursiveNameServer))

	iapd := &dhcpv6.OptIAPD{IaId: iaid}

	switch {
	case lease != nil && lease.iapd != nil:
		iapd.Options = lease.iapd.Options
	case lease != nil:
		iapd.Options.Add(&dhcpv6.OptIAPrefix{Prefix: ipnet(lease.Prefix)})
	case hint != 0:
		iapd.Options.Add(&dhcpv6.OptIAPrefix{Prefix: &net.IPNet{IP: net.IPv6zero, Mask: net.CIDRMask(hint, 128)}})
	}

	m.AddOption(iapd)

	return m, nil
}

// ErrRefused is returned by Parse when the server answered without a usable prefix (a status such
// as NoBinding, or lifetimes of 0): the lease is gone and the client must solicit again.
var ErrRefused = errors.New("server")

// MinRenew is the shortest time before a renewal.
const MinRenew = 30 * time.Second

// Parse reads the delegated prefix from an Advertise or Reply.
func Parse(m *dhcpv6.Message, iaid [4]byte, now time.Time) (*Lease, error) {
	if st := m.Options.Status(); st != nil && st.StatusCode != iana.StatusSuccess {
		return nil, fmt.Errorf("%w: %s", ErrRefused, st)
	}

	var iapd *dhcpv6.OptIAPD

	for _, o := range m.Options.IAPD() {
		if o.IaId == iaid {
			iapd = o
		}
	}

	if iapd == nil {
		return nil, errors.New("no IA_PD in the answer")
	}

	if st := iapd.Options.Status(); st != nil && st.StatusCode != iana.StatusSuccess {
		return nil, fmt.Errorf("%w: %s", ErrRefused, st)
	}

	sid := m.Options.ServerID()
	if sid == nil {
		return nil, errors.New("no server identifier in the answer")
	}

	for _, p := range iapd.Options.Prefixes() {
		prefix, ok := toPrefix(p.Prefix)
		if !ok || p.ValidLifetime == 0 || prefix.Bits() > 64 {
			continue
		}

		l := &Lease{
			Prefix: prefix, Preferred: p.PreferredLifetime, Valid: p.ValidLifetime,
			T1: iapd.T1, T2: iapd.T2, Obtained: now, ServerID: sid.ToBytes(),
			iapd: iapd,
		}

		// RFC 8415 14.2: T1/T2 of 0 leave the timing to the client
		if l.T1 == 0 || l.T1 > l.Preferred {
			l.T1 = l.Preferred / 2
		}

		if l.T2 == 0 || l.T2 < l.T1 {
			l.T2 = l.Preferred * 4 / 5
		}

		// a deprecated prefix (preferred 0) or tiny timers must not make the client renew in a loop
		l.T1 = max(l.T1, MinRenew)
		l.T2 = max(l.T2, l.T1)

		return l, nil
	}

	return nil, fmt.Errorf("%w: no usable prefix in the IA_PD", ErrRefused)
}

// Subnet returns the /64 number id of the delegated prefix, and the router's address in it (::1).
func Subnet(delegated netip.Prefix, id uint32) (netip.Prefix, netip.Addr, error) {
	if delegated.Bits() > 64 || (64-delegated.Bits() < 32 && id >= 1<<(64-delegated.Bits())) {
		return netip.Prefix{}, netip.Addr{}, fmt.Errorf("subnet id %d does not fit in %s", id, delegated)
	}

	b := delegated.Masked().Addr().As16()
	hi := binary.BigEndian.Uint64(b[:8]) | uint64(id)
	binary.BigEndian.PutUint64(b[:8], hi)

	subnet := netip.PrefixFrom(netip.AddrFrom16(b), 64)
	b[15] = 1

	return subnet, netip.AddrFrom16(b), nil
}

// Remaining returns the preferred and valid lifetimes left at now.
func (l *Lease) Remaining(now time.Time) (preferred, valid time.Duration) {
	elapsed := now.Sub(l.Obtained)

	return max(l.Preferred-elapsed, 0), max(l.Valid-elapsed, 0)
}

func ipnet(p netip.Prefix) *net.IPNet {
	return &net.IPNet{IP: p.Addr().AsSlice(), Mask: net.CIDRMask(p.Bits(), 128)}
}

func toPrefix(n *net.IPNet) (netip.Prefix, bool) {
	if n == nil {
		return netip.Prefix{}, false
	}

	a, ok := netip.AddrFromSlice(n.IP)
	if !ok || !a.Is6() || a.Is4In6() {
		return netip.Prefix{}, false
	}

	bits, _ := n.Mask.Size()

	return netip.PrefixFrom(a, bits).Masked(), true
}
