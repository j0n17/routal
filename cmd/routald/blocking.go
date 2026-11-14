package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/j0n17/routal/pkg/blocklist"
	"github.com/j0n17/routal/pkg/config"
	"github.com/j0n17/routal/pkg/services"
)

const maxListSize = 64 << 20

// blocker keeps unbound's blocking zones in line with DNSServerConfig.blocking. Lists are
// downloaded in the background and cached in the state directory (a failed refresh keeps the
// previous copy); zones are pushed with unbound-control, so updates do not flush the DNS cache.
type blocker struct {
	ctl  string
	dirs services.Dirs

	mu       sync.Mutex
	fetching map[string]bool
	tried    map[string]time.Time

	inputs      string // what block/allow were computed from
	block       []string
	allow       []string
	gen         int
	pushed      map[string]string // zone -> type
	lastSummary string
}

func (b *blocker) sync(cfg *config.Config, unboundGen int) error {
	var bl *config.DNSBlocking
	if cfg.DNSServer != nil {
		bl = cfg.DNSServer.Blocking
	}

	if unboundGen == 0 {
		b.pushed = nil

		return nil
	}

	if unboundGen != b.gen {
		b.gen, b.pushed = unboundGen, map[string]string{}
	}

	want := map[string]string{}

	if bl != nil {
		b.refresh(bl)

		if key := b.inputKey(bl); key != b.inputs {
			b.compute(bl)
			b.inputs = key
		}

		typ := map[string]string{"nxdomain": "always_nxdomain", "null": "always_null"}[bl.Response]
		for _, n := range b.block {
			want[n] = typ
		}

		for _, n := range b.allow {
			want[n] = "transparent"
		}
	}

	var remove, add []string

	for zone, typ := range b.pushed {
		if want[zone] != typ {
			remove = append(remove, zone+".")
		}
	}

	for zone, typ := range want {
		if b.pushed[zone] != typ {
			add = append(add, zone+". "+typ)
		}
	}

	conf := filepath.Join(b.dirs.State, "unbound.conf")

	if len(remove) > 0 {
		if err := unboundControl(b.ctl, conf, "local_zones_remove", remove); err != nil {
			return err
		}
	}

	if len(add) > 0 {
		if err := unboundControl(b.ctl, conf, "local_zones", add); err != nil {
			return err
		}
	}

	b.pushed = want

	if s := fmt.Sprintf("%d blocked zones, %d allowed", len(b.block), len(b.allow)); bl != nil && s != b.lastSummary {
		b.lastSummary = s
		log.Printf("blocking: %s", s)
	}

	return nil
}

func (b *blocker) cachePath(url string) string {
	sum := sha256.Sum256([]byte(url))

	return filepath.Join(b.dirs.State, "blocklists", hex.EncodeToString(sum[:8])+".txt")
}

// refresh downloads the lists that are missing or older than Refresh, in the background.
func (b *blocker) refresh(bl *config.DNSBlocking) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.fetching == nil {
		b.fetching, b.tried = map[string]bool{}, map[string]time.Time{}
	}

	for _, url := range bl.Lists {
		if b.fetching[url] {
			continue
		}

		fresh := false
		if st, err := os.Stat(b.cachePath(url)); err == nil && time.Since(st.ModTime()) < bl.Refresh {
			fresh = true
		}

		// after a failure, retry every 5 minutes at most
		if fresh || time.Since(b.tried[url]) < 5*time.Minute {
			continue
		}

		b.fetching[url], b.tried[url] = true, time.Now()

		go func() {
			err := download(url, b.cachePath(url))

			b.mu.Lock()
			delete(b.fetching, url)
			b.mu.Unlock()

			if err != nil {
				log.Printf("blocking: %s: %v (keeping the previous copy)", url, err)
			}
		}()
	}
}

func (b *blocker) inputKey(bl *config.DNSBlocking) string {
	h := sha256.New()
	fmt.Fprintf(h, "%v|%v|%v", bl.Lists, bl.Domains, bl.Allow)

	for _, url := range bl.Lists {
		if st, err := os.Stat(b.cachePath(url)); err == nil {
			fmt.Fprintf(h, "|%d|%d", st.ModTime().UnixNano(), st.Size())
		}
	}

	return hex.EncodeToString(h.Sum(nil))
}

func (b *blocker) compute(bl *config.DNSBlocking) {
	var names []string

	for _, url := range bl.Lists {
		f, err := os.Open(b.cachePath(url))
		if err != nil {
			continue // not downloaded yet
		}

		names = append(names, blocklist.Parse(f)...)
		f.Close() //nolint:errcheck
	}

	for _, d := range bl.Domains {
		if n, ok := blocklist.Normalize(d); ok {
			names = append(names, n)
		}
	}

	b.block, b.allow = blocklist.Zones(names, bl.Allow)
}

func download(url, path string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}

	resp, err := httpClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %s", resp.Status)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxListSize+1))
	if err != nil {
		return err
	}

	if len(data) > maxListSize {
		return fmt.Errorf("larger than %d MiB", maxListSize>>20)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	if err := os.WriteFile(path+".new", data, 0o644); err != nil {
		return err
	}

	return os.Rename(path+".new", path)
}

// httpClient resolves names with Talos' host DNS when the container has no resolv.conf.
func httpClient() *http.Client {
	if _, err := os.Stat("/etc/resolv.conf"); err == nil {
		return &http.Client{}
	}

	resolver := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, services.HostDNS+":53")
	}}

	return &http.Client{Transport: &http.Transport{DialContext: (&net.Dialer{Resolver: resolver}).DialContext}}
}

func unboundControl(ctl, conf, cmd string, lines []string) error {
	c := exec.Command(ctl, "-c", conf, cmd)
	c.Stdin = strings.NewReader(strings.Join(lines, "\n") + "\n")

	var out bytes.Buffer

	c.Stdout, c.Stderr = &out, &out

	if err := c.Run(); err != nil {
		return fmt.Errorf("unbound-control %s: %w: %s", cmd, err, strings.TrimSpace(out.String()))
	}

	return nil
}
