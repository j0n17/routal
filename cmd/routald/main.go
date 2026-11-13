// routald is the Routal router agent, shipped as a Talos system extension.
//
//	routald run [-config /etc/routal/router.yaml]   apply and keep reconciling (extension entrypoint)
//	routald validate <router.yaml>                    check a config
//	routald render <router.yaml>                      print the nftables ruleset
//	routald talos-patch <router.yaml>                 print the Talos machine config patch
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/routal/routal/pkg/config"
	"github.com/routal/routal/pkg/nft"
	"github.com/routal/routal/pkg/talospatch"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "routald:", err)
		os.Exit(1)
	}
}

func usage() error {
	return fmt.Errorf("usage: routald run|validate|render|talos-patch [args]")
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
		_ = fs.Parse(args)

		ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer cancel()

		return (&agent{configPath: *path, nftBin: *nftBin}).run(ctx)
	}

	if len(args) != 1 {
		return usage()
	}

	raw, err := os.ReadFile(args[0])
	if err != nil {
		return err
	}

	cfg, err := config.Load(raw)
	if err != nil {
		return fmt.Errorf("%s: %w", args[0], err)
	}

	switch cmd {
	case "validate":
		fmt.Printf("%s is valid\n", args[0])
	case "render":
		fmt.Print(nft.Render(cfg, nil))
	case "talos-patch":
		out, err := talospatch.Generate(raw, cfg)
		if err != nil {
			return err
		}

		os.Stdout.Write(out) //nolint:errcheck
	default:
		return usage()
	}

	return nil
}
