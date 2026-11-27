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

// Dirs are the writable directories of the extension container. Both belong to root: a daemon only
// writes in its own subdirectory (Data, Runtime), so that it cannot touch another daemon's files or
// the configs routald writes.
type Dirs struct {
	State string // persistent (daemon configs; leases, drift in the subdirectories): /var/lib/routal
	Run   string // volatile (pid files, control sockets, in the subdirectories): /run/routal
}

// Data is the persistent directory of a daemon.
func (d Dirs) Data(name string) string { return d.State + "/" + name }

// Runtime is the volatile directory of a daemon.
func (d Dirs) Runtime(name string) string { return d.Run + "/" + name }

// Linux capabilities daemons keep after leaving root (ambient).
const (
	CapNetBindService uintptr = 10 // ports below 1024
	CapNetAdmin       uintptr = 12 // nftables
	CapNetRaw         uintptr = 13 // raw and packet sockets
)

// DefaultDirs are the directories inside the extension.
var DefaultDirs = Dirs{State: "/var/lib/routal", Run: "/run/routal"}

// AvahiRunDir is where avahi-daemon keeps its pid file and socket, whatever its configuration.
const AvahiRunDir = "/run/avahi-daemon"

// Service is one supervised daemon with its rendered config.
type Service struct {
	Name    string
	File    string // config file name in Dirs.State
	Config  string
	Command []string // "{conf}" is replaced by the config path
	Test    []string // config check, nil when the daemon has none
	Env     []string
	// User is the account the daemon runs as, with Caps as its only capabilities; "" is root. Dir
	// names its directories (Dirs.Data, Dirs.Runtime), which belong to User.
	User string
	Caps []uintptr
	Dir  string
	// OwnDirs are directories outside Dirs that the daemon has compiled in, created for User.
	OwnDirs  []string
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
			// packet sockets (clients without an address yet) and port 67
			User: "kea", Dir: keaDir, Caps: []uintptr{CapNetRaw, CapNetBindService},
		})
	}

	if needsDHCPv6(cfg) {
		conf, warn := kea6(cfg, e, d)
		out = append(out, Service{
			Name: "kea-dhcp6", File: "kea-dhcp6.json", Config: conf, Warnings: warn,
			Command: []string{"/usr/sbin/kea-dhcp6", "-c", "{conf}"},
			Test:    []string{"/usr/sbin/kea-dhcp6", "-t", "{conf}"},
			Env:     keaEnv(d),
			User:    "kea", Dir: keaDir, Caps: []uintptr{CapNetBindService},
		})
	}

	if cfg.DNSServer != nil {
		conf, warn := unbound(cfg, e, d)
		out = append(out, Service{
			Name: "unbound", File: "unbound.conf", Config: conf, Warnings: warn,
			Command: []string{"/usr/sbin/unbound", "-d", "-p", "-c", "{conf}"},
			Test:    []string{"/usr/sbin/unbound-checkconf", "{conf}"},
			User:    "unbound", Dir: unboundDir, Caps: []uintptr{CapNetBindService},
		})
	}

	if len(cfg.RouterAdvertisements) > 0 {
		conf, warn := radvd(cfg, e)
		out = append(out, Service{
			Name: "radvd", File: "radvd.conf", Config: conf, Warnings: warn,
			Command: []string{"/usr/sbin/radvd", "-n", "-m", "stderr", "-C", "{conf}", "-p", d.Runtime("radvd") + "/radvd.pid"},
			Test:    []string{"/usr/sbin/radvd", "-c", "-C", "{conf}"},
			User:    "radvd", Dir: "radvd", Caps: []uintptr{CapNetRaw}, // ICMPv6 raw socket
		})
	}

	if cfg.NTPServer != nil {
		conf, warn := chrony(cfg, e, d)
		out = append(out, Service{
			Name: "chronyd", File: "chrony.conf", Config: conf, Warnings: warn,
			// -x: serve time without touching the clock (Talos owns it); -U: started without root
			Command: []string{"/usr/sbin/chronyd", "-d", "-x", "-U", "-f", "{conf}"},
			Test:    []string{"/usr/sbin/chronyd", "-p", "-f", "{conf}"},
			User:    "chrony", Dir: "chrony", Caps: []uintptr{CapNetBindService},
		})
	}

	if cfg.MDNSReflector != nil {
		out = append(out, Service{
			Name: "avahi-daemon", File: "avahi-daemon.conf", Config: avahi(cfg, e),
			Command: []string{"/usr/sbin/avahi-daemon", "-f", "{conf}", "--no-drop-root", "--no-chroot", "--no-rlimits"},
			User:    "avahi", Dir: "avahi", OwnDirs: []string{AvahiRunDir},
		})
	}

	if cfg.UPnP != nil {
		conf, warn := miniupnpd(cfg, e, d)
		out = append(out, Service{
			Name: "miniupnpd", File: "miniupnpd.conf", Config: conf, Warnings: warn,
			Command: []string{"/usr/sbin/miniupnpd", "-d", "-f", "{conf}", "-P", d.Runtime("miniupnpd") + "/miniupnpd.pid"},
			// it adds its mappings to nftables itself
			User: "miniupnpd", Dir: "miniupnpd", Caps: []uintptr{CapNetAdmin, CapNetRaw},
		})
	}

	for _, p := range cfg.PPPoE {
		out = append(out, Service{
			Name: "pppd-" + p.Name, File: "pppoe-" + p.Name + ".options", Config: pppd(p, e),
			// root: pppd creates the interface, sets its addresses and the default route
			Command: []string{"/usr/sbin/pppd", "file", "{conf}"},
		})
	}

	if len(cfg.DynamicDNS) > 0 {
		out = append(out, Service{
			Name: "inadyn", File: "inadyn.conf", Config: inadyn(cfg),
			Command: []string{"/usr/sbin/inadyn", "--foreground", "-f", "{conf}", "--cache-dir", d.Data("inadyn"), "-P", d.Runtime("inadyn") + "/inadyn.pid"},
			Test:    []string{"/usr/sbin/inadyn", "--check-config", "-f", "{conf}"},
			User:    "inadyn", Dir: "inadyn",
		})
	}

	return out
}

// Kea >= 2.6.3 refuses lease files outside its data directory unless told where it is.
func keaEnv(d Dirs) []string {
	return []string{"KEA_PIDFILE_DIR=" + d.Runtime(keaDir), "KEA_LOCKFILE_DIR=" + d.Runtime(keaDir), "KEA_DHCP_DATA_DIR=" + d.Data(keaDir)}
}
