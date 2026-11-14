// Package services renders the configs of the daemons routald supervises inside the extension:
//
//	kea-dhcp4     DHCPServerConfig
//	kea-dhcp6     RouterAdvertisementConfig with other: true (stateless DHCPv6 options)
//	unbound       DNSServerConfig (caching forwarder, local domain; DHCP names are pushed at runtime)
//	radvd         RouterAdvertisementConfig
//	chronyd       NTPServerConfig
//	avahi-daemon  MDNSReflectorConfig
//	miniupnpd     UPnPConfig
//	pppd          PPPoEConfig (one per session)
//	inadyn        DynamicDNSConfig
package services

import (
	"github.com/j0n17/routal/pkg/config"
	"github.com/j0n17/routal/pkg/env"
)

// Dirs are the writable directories of the extension container.
type Dirs struct {
	State string // persistent (leases, drift): /var/lib/routal
	Run   string // volatile (pid files, control sockets): /run/routal
}

// DefaultDirs are the directories inside the extension.
var DefaultDirs = Dirs{State: "/var/lib/routal", Run: "/run/routal"}

// Service is one supervised daemon with its rendered config.
type Service struct {
	Name     string
	File     string // config file name in Dirs.State
	Config   string
	Command  []string // "{conf}" is replaced by the config path
	Test     []string // config check, nil when the daemon has none
	Env      []string
	Warnings []string // parts of the config that could not be rendered yet (e.g. link has no address)
}

// Render returns the services the config needs, in a stable order.
func Render(cfg *config.Config, e env.Env, d Dirs) []Service {
	var out []Service

	if len(cfg.DHCPServers) > 0 {
		conf, warn := kea4(cfg, e, d)
		out = append(out, Service{
			Name: "kea-dhcp4", File: "kea-dhcp4.json", Config: conf, Warnings: warn,
			Command: []string{"/usr/sbin/kea-dhcp4", "-c", "{conf}"},
			Test:    []string{"/usr/sbin/kea-dhcp4", "-t", "{conf}"},
			Env:     keaEnv(d),
		})
	}

	if needsDHCPv6(cfg) {
		conf, warn := kea6(cfg, e, d)
		out = append(out, Service{
			Name: "kea-dhcp6", File: "kea-dhcp6.json", Config: conf, Warnings: warn,
			Command: []string{"/usr/sbin/kea-dhcp6", "-c", "{conf}"},
			Test:    []string{"/usr/sbin/kea-dhcp6", "-t", "{conf}"},
			Env:     keaEnv(d),
		})
	}

	if cfg.DNSServer != nil {
		conf, warn := unbound(cfg, e, d)
		out = append(out, Service{
			Name: "unbound", File: "unbound.conf", Config: conf, Warnings: warn,
			Command: []string{"/usr/sbin/unbound", "-d", "-p", "-c", "{conf}"},
			Test:    []string{"/usr/sbin/unbound-checkconf", "{conf}"},
		})
	}

	if len(cfg.RouterAdvertisements) > 0 {
		conf, warn := radvd(cfg, e)
		out = append(out, Service{
			Name: "radvd", File: "radvd.conf", Config: conf, Warnings: warn,
			Command: []string{"/usr/sbin/radvd", "-n", "-m", "stderr", "-C", "{conf}", "-p", d.Run + "/radvd.pid"},
			Test:    []string{"/usr/sbin/radvd", "-c", "-C", "{conf}"},
		})
	}

	if cfg.NTPServer != nil {
		conf, warn := chrony(cfg, e, d)
		out = append(out, Service{
			Name: "chronyd", File: "chrony.conf", Config: conf, Warnings: warn,
			// -x: serve time without touching the clock (Talos owns it)
			Command: []string{"/usr/sbin/chronyd", "-d", "-x", "-u", "root", "-f", "{conf}"},
			Test:    []string{"/usr/sbin/chronyd", "-p", "-f", "{conf}"},
		})
	}

	if cfg.MDNSReflector != nil {
		out = append(out, Service{
			Name: "avahi-daemon", File: "avahi-daemon.conf", Config: avahi(cfg, e),
			Command: []string{"/usr/sbin/avahi-daemon", "-f", "{conf}", "--no-drop-root", "--no-chroot", "--no-rlimits"},
		})
	}

	if cfg.UPnP != nil {
		conf, warn := miniupnpd(cfg, e, d)
		out = append(out, Service{
			Name: "miniupnpd", File: "miniupnpd.conf", Config: conf, Warnings: warn,
			Command: []string{"/usr/sbin/miniupnpd", "-d", "-f", "{conf}", "-P", d.Run + "/miniupnpd.pid"},
		})
	}

	for _, p := range cfg.PPPoE {
		out = append(out, Service{
			Name: "pppd-" + p.Name, File: "pppoe-" + p.Name + ".options", Config: pppd(p, e),
			Command: []string{"/usr/sbin/pppd", "file", "{conf}"},
		})
	}

	if len(cfg.DynamicDNS) > 0 {
		out = append(out, Service{
			Name: "inadyn", File: "inadyn.conf", Config: inadyn(cfg),
			Command: []string{"/usr/sbin/inadyn", "--foreground", "-f", "{conf}", "--cache-dir", d.State + "/inadyn", "-P", d.Run + "/inadyn.pid"},
			Test:    []string{"/usr/sbin/inadyn", "--check-config", "-f", "{conf}"},
		})
	}

	return out
}

// Kea >= 2.6.3 refuses lease files outside its data directory unless told where it is.
func keaEnv(d Dirs) []string {
	return []string{"KEA_PIDFILE_DIR=" + d.Run, "KEA_LOCKFILE_DIR=" + d.Run, "KEA_DHCP_DATA_DIR=" + d.State}
}
