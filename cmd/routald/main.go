// routald is the Routal router agent, shipped as a Talos system extension.
//
//	routald run [-config /etc/routal/router.yaml]   apply and keep reconciling (extension entrypoint)
//	routald validate <router.yaml>                    check a config
//	routald render <router.yaml>                      print the nftables ruleset
//	routald render-services <router.yaml> <dir>       write the daemon configs (Kea, Unbound, radvd...) to dir
//	routald talos-patch [-current mc.yaml] <router.yaml>  print the Talos machine config patch
//	routald addr [-6] <link>                          print the link's public address (dynamic DNS)
//	routald talos-render <talos.yaml>                 print talos.yaml with privateKeyFile & co. resolved
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/j0n17/routal/pkg/config"
	"github.com/j0n17/routal/pkg/env"
	"github.com/j0n17/routal/pkg/nft"
	"github.com/j0n17/routal/pkg/services"
	"github.com/j0n17/routal/pkg/talospatch"
	"github.com/j0n17/routal/pkg/talosrender"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "routald:", err)
		os.Exit(1)
	}
}

func usage() error {
	return fmt.Errorf("usage: routald run|validate|render|render-services|talos-patch|talos-render|addr [args]")
}

func run(args []string) error {
	if len(args) == 0 {
		return usage()
	}

	cmd, args := args[0], args[1:]

	if cmd == "run" {
		fs := flag.NewFlagSet("run", flag.ExitOnError)
		path := fs.String("config", talospatch.ConfigPath, "router config file")
		nftBin := fs.String("nft", "/usr/sbin/nft", "nft binary (extension services run with an empty PATH)")
		stateDir := fs.String("state-dir", services.DefaultDirs.State, "persistent directory (daemon configs, leases)")
		runDir := fs.String("run-dir", services.DefaultDirs.Run, "volatile directory (pid files, control sockets)")
		_ = fs.Parse(args)

		ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer cancel()

		dirs := services.Dirs{State: *stateDir, Run: *runDir}

		return (&agent{
			configPath: *path,
			nftBin:     *nftBin,
			dirs:       dirs,
			services:   newSupervisor(dirs),
			names:      &leaseNames{ctl: "/usr/sbin/unbound-control", dirs: dirs},
			pd:         &pdManager{stateDir: dirs.State},
			blocker:    &blocker{ctl: "/usr/sbin/unbound-control", dirs: dirs},
		}).run(ctx)
	}

	if cmd == "addr" {
		fs := flag.NewFlagSet("addr", flag.ExitOnError)
		v6 := fs.Bool("6", false, "IPv6 (global, not ULA) instead of IPv4")
		_ = fs.Parse(args)

		if fs.NArg() != 1 {
			return usage()
		}

		for _, p := range env.Host().Addrs(fs.Arg(0)) {
			a := p.Addr()
			if a.Is4() != *v6 && !a.IsLoopback() && !a.IsLinkLocalUnicast() && !(a.Is6() && a.IsPrivate()) {
				fmt.Println(a)

				return nil
			}
		}

		return fmt.Errorf("no address on %s", fs.Arg(0))
	}

	if cmd == "talos-render" {
		if len(args) != 1 {
			return usage()
		}

		raw, err := os.ReadFile(args[0])
		if err != nil {
			return err
		}

		out, err := talosrender.Render(raw, filepath.Dir(args[0]))
		if err != nil {
			return fmt.Errorf("%s: %w", args[0], err)
		}

		_, err = os.Stdout.Write(out)

		return err
	}

	var (
		current []byte
		outDir  string
	)

	if cmd == "talos-patch" {
		fs := flag.NewFlagSet("talos-patch", flag.ExitOnError)
		currentPath := fs.String("current", "", "current machine config (`talosctl get mc -o yaml`), to delete the rules it no longer needs")
		_ = fs.Parse(args)
		args = fs.Args()

		if *currentPath != "" {
			var err error
			if current, err = os.ReadFile(*currentPath); err != nil {
				return err
			}
		}
	}

	if cmd == "render-services" {
		if len(args) != 2 {
			return usage()
		}

		args, outDir = args[:1], args[1]
	}

	if len(args) != 1 {
		return usage()
	}

	raw, err := os.ReadFile(args[0])
	if err != nil {
		return err
	}

	cfg, err := config.LoadDir(raw, filepath.Dir(args[0]))
	if err != nil {
		return fmt.Errorf("%s: %w", args[0], err)
	}

	switch cmd {
	case "validate":
		fmt.Printf("%s is valid\n", args[0])
	case "render":
		fmt.Print(nft.Render(cfg, env.Static()))
	case "render-services":
		dir := outDir
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}

		for _, svc := range services.Render(cfg, env.Static(), services.DefaultDirs) {
			if err := os.WriteFile(filepath.Join(dir, svc.File), []byte(svc.Config), 0o644); err != nil {
				return err
			}

			fmt.Printf("%s: %s\n", svc.Name, filepath.Join(dir, svc.File))
		}
	case "talos-patch":
		out, err := talospatch.Generate(raw, cfg, current)
		if err != nil {
			return err
		}

		os.Stdout.Write(out) //nolint:errcheck
	default:
		return usage()
	}

	return nil
}
