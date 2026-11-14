package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/insomniacslk/dhcp/dhcpv6"
	"github.com/insomniacslk/dhcp/dhcpv6/nclient6"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"

	"github.com/j0n17/routal/pkg/config"
	"github.com/j0n17/routal/pkg/env"
	"github.com/j0n17/routal/pkg/pd"
)

// pdManager runs one DHCPv6 prefix delegation client per PrefixDelegationConfig, restarting it
// when its document changes.
type pdManager struct {
	stateDir string
	clients  map[string]*pdClient
}

type pdClient struct {
	cfg    config.PrefixDelegation
	sum    [32]byte
	cancel context.CancelFunc
	done   chan struct{}
}

func (m *pdManager) reconcile(cfg *config.Config) {
	if m.clients == nil {
		m.clients = map[string]*pdClient{}
	}

	want := map[string]bool{}

	for _, c := range cfg.PrefixDelegations {
		want[c.Name] = true
		raw, _ := json.Marshal(c)
		sum := sha256.Sum256(raw)

		if old := m.clients[c.Name]; old != nil {
			if old.sum == sum {
				continue
			}

			old.stop()
		}

		ctx, cancel := context.WithCancel(context.Background())
		cl := &pdClient{cfg: c, sum: sum, cancel: cancel, done: make(chan struct{})}
		m.clients[c.Name] = cl

		go func() {
			defer close(cl.done)
			cl.run(ctx, m.stateDir)
		}()
	}

	for name, cl := range m.clients {
		if !want[name] {
			cl.stop()
			delete(m.clients, name)
		}
	}
}

func (m *pdManager) stopAll() {
	for _, cl := range m.clients {
		cl.stop()
	}
}

func (c *pdClient) stop() {
	c.cancel()
	<-c.done
}

func (c *pdClient) logf(format string, args ...any) {
	log.Printf("prefix delegation %s: %s", c.cfg.Name, fmt.Sprintf(format, args...))
}

// run keeps a delegated prefix: solicit, apply, renew at T1, rebind at T2, start over when it
// expires. Addresses carry the lease lifetimes, so the kernel drops them if routald is gone.
func (c *pdClient) run(ctx context.Context, stateDir string) {
	iaid := pd.IAID(c.cfg.Name)
	backoff := 5 * time.Second

	var applied *pd.Lease

	for ctx.Err() == nil {
		link := env.ResolveLink(c.cfg.Link)

		if *c.cfg.AcceptRA {
			// forwarding turns off RA processing (accept_ra=1); 2 keeps the ISP's default route
			_ = os.WriteFile(filepath.Join("/proc/sys/net/ipv6/conf", link, "accept_ra"), []byte("2"), 0o644)
		}

		client, duid, err := c.open(link, stateDir)
		if err != nil {
			c.logf("waiting for %s: %v", c.cfg.Link, err)
			sleep(ctx, backoff)

			continue
		}

		lease, err := c.obtain(ctx, client, duid, iaid)
		if err != nil {
			client.Close() //nolint:errcheck
			c.logf("no prefix yet: %v", err)
			sleep(ctx, backoff)
			backoff = min(backoff*2, 5*time.Minute)

			continue
		}

		backoff = 5 * time.Second

		for lease != nil && ctx.Err() == nil {
			if err := c.apply(lease, applied, stateDir); err != nil {
				c.logf("applying %s: %v", lease.Prefix, err)
			}

			applied = lease
			lease = c.keep(ctx, client, duid, iaid, lease)
		}

		client.Close() //nolint:errcheck
	}

	c.logf("stopped")
}

func (c *pdClient) open(link, stateDir string) (*nclient6.Client, dhcpv6.DUID, error) {
	iface, err := net.InterfaceByName(link)
	if err != nil {
		return nil, nil, err
	}

	duid, err := loadDUID(filepath.Join(stateDir, "dhcpv6-duid-"+c.cfg.Name), iface.HardwareAddr)
	if err != nil {
		return nil, nil, err
	}

	conn, err := nclient6.NewIPv6UDPConn(link, dhcpv6.DefaultClientPort)
	if err != nil {
		return nil, nil, err // no link-local address yet
	}

	server := &net.UDPAddr{IP: dhcpv6.AllDHCPRelayAgentsAndServers, Port: dhcpv6.DefaultServerPort, Zone: link}

	client, err := nclient6.NewWithConn(conn, iface.HardwareAddr, nclient6.WithBroadcastAddr(server),
		nclient6.WithTimeout(3*time.Second), nclient6.WithRetry(3))
	if err != nil {
		conn.Close() //nolint:errcheck

		return nil, nil, err
	}

	return client, duid, nil
}

// obtain runs Solicit / Advertise / Request / Reply.
func (c *pdClient) obtain(ctx context.Context, client *nclient6.Client, duid dhcpv6.DUID, iaid [4]byte) (*pd.Lease, error) {
	sol, err := pd.Message(dhcpv6.MessageTypeSolicit, duid, iaid, c.cfg.PrefixLength, nil)
	if err != nil {
		return nil, err
	}

	adv, err := client.SendAndRead(ctx, client.RemoteAddr(), sol, nclient6.IsMessageType(dhcpv6.MessageTypeAdvertise))
	if err != nil {
		return nil, fmt.Errorf("solicit: %w", err)
	}

	offer, err := pd.Parse(adv, iaid, time.Now())
	if err != nil {
		return nil, fmt.Errorf("advertise: %w", err)
	}

	return c.exchange(ctx, client, dhcpv6.MessageTypeRequest, duid, iaid, offer)
}

func (c *pdClient) exchange(ctx context.Context, client *nclient6.Client, t dhcpv6.MessageType, duid dhcpv6.DUID, iaid [4]byte, l *pd.Lease) (*pd.Lease, error) {
	msg, err := pd.Message(t, duid, iaid, 0, l)
	if err != nil {
		return nil, err
	}

	reply, err := client.SendAndRead(ctx, client.RemoteAddr(), msg, nclient6.IsMessageType(dhcpv6.MessageTypeReply))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", t, err)
	}

	return pd.Parse(reply, iaid, time.Now())
}

// keep waits for T1 and renews, then T2 and rebinds, until the lease expires (nil).
func (c *pdClient) keep(ctx context.Context, client *nclient6.Client, duid dhcpv6.DUID, iaid [4]byte, l *pd.Lease) *pd.Lease {
	for _, step := range []struct {
		at time.Duration
		t  dhcpv6.MessageType
	}{{l.T1, dhcpv6.MessageTypeRenew}, {l.T2, dhcpv6.MessageTypeRebind}} {
		for {
			wait := time.Until(l.Obtained.Add(step.at))
			if wait > 0 && !sleep(ctx, wait) {
				return nil
			}

			if _, valid := l.Remaining(time.Now()); valid == 0 {
				c.logf("%s expired", l.Prefix)

				return nil
			}

			next, err := c.exchange(ctx, client, step.t, duid, iaid, l)
			if err == nil {
				return next
			}

			c.logf("%v (retrying)", err)

			if step.t == dhcpv6.MessageTypeRenew && time.Now().After(l.Obtained.Add(l.T2)) {
				break // time to rebind
			}

			if !sleep(ctx, 30*time.Second) {
				return nil
			}
		}
	}

	_, valid := l.Remaining(time.Now())
	sleep(ctx, min(valid, 30*time.Second))

	return nil
}

// apply gives each subnet link its /64 (router ::1) and routes the rest of the delegated prefix to
// nowhere, so that unused parts never loop back to the ISP.
func (c *pdClient) apply(l, old *pd.Lease, stateDir string) error {
	preferred, valid := l.Remaining(time.Now())

	if old != nil && old.Prefix != l.Prefix {
		c.logf("prefix changed from %s to %s", old.Prefix, l.Prefix)
		c.remove(old)
	}

	var errs []error

	for _, s := range c.cfg.Subnets {
		subnet, addr, err := pd.Subnet(l.Prefix, s.ID)
		if err != nil {
			errs = append(errs, err)

			continue
		}

		link, err := netlink.LinkByName(env.ResolveLink(s.Link))
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", s.Link, err))

			continue
		}

		a := &netlink.Addr{
			IPNet:       &net.IPNet{IP: addr.AsSlice(), Mask: net.CIDRMask(subnet.Bits(), 128)},
			PreferedLft: int(preferred / time.Second),
			ValidLft:    int(valid / time.Second),
		}

		if err := netlink.AddrReplace(link, a); err != nil {
			errs = append(errs, fmt.Errorf("%s on %s: %w", addr, s.Link, err))
		}
	}

	unreachable := &netlink.Route{
		Dst:  &net.IPNet{IP: l.Prefix.Addr().AsSlice(), Mask: net.CIDRMask(l.Prefix.Bits(), 128)},
		Type: unix.RTN_UNREACHABLE, Priority: 2048,
	}
	if err := netlink.RouteReplace(unreachable); err != nil {
		errs = append(errs, fmt.Errorf("unreachable route: %w", err))
	}

	raw, _ := json.MarshalIndent(l, "", "  ")
	_ = os.WriteFile(filepath.Join(stateDir, "pd-"+c.cfg.Name+".json"), raw, 0o644)

	c.logf("%s (preferred %s, valid %s, renew in %s)", l.Prefix, preferred, valid, time.Until(l.Obtained.Add(l.T1)).Round(time.Second))

	return errors.Join(errs...)
}

func (c *pdClient) remove(l *pd.Lease) {
	for _, s := range c.cfg.Subnets {
		subnet, addr, err := pd.Subnet(l.Prefix, s.ID)
		if err != nil {
			continue
		}

		if link, err := netlink.LinkByName(env.ResolveLink(s.Link)); err == nil {
			_ = netlink.AddrDel(link, &netlink.Addr{IPNet: &net.IPNet{IP: addr.AsSlice(), Mask: net.CIDRMask(subnet.Bits(), 128)}})
		}
	}

	_ = netlink.RouteDel(&netlink.Route{
		Dst:  &net.IPNet{IP: l.Prefix.Addr().AsSlice(), Mask: net.CIDRMask(l.Prefix.Bits(), 128)},
		Type: unix.RTN_UNREACHABLE, Priority: 2048,
	})
}

// loadDUID returns the persisted DUID, or creates it (from the MAC when the link has one).
func loadDUID(path string, mac net.HardwareAddr) (dhcpv6.DUID, error) {
	if raw, err := os.ReadFile(path); err == nil {
		return pd.ParseDUID(raw)
	}

	duid := pd.DUID(mac)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}

	return duid, os.WriteFile(path, duid.ToBytes(), 0o644)
}

// sleep waits for d or the context; false when the context is done.
func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()

	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
