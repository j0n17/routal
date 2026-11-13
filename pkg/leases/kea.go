// Package leases reads the DHCP leases of the Kea memfile backend.
package leases

import (
	"encoding/csv"
	"errors"
	"io"
	"io/fs"
	"net/netip"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Lease is an active lease with a usable hostname.
type Lease struct {
	Address  netip.Addr
	Hostname string // a single DNS label, lower case
}

// clients choose their hostname: only plain labels are published (no injection into DNS records)
var labelRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// ReadKea4 returns the active leases with a hostname. Kea appends to the lease file and its cleanup
// process (LFC) rotates it, so the files are read oldest first and the last line for an address wins.
func ReadKea4(path string) ([]Lease, error) {
	return readKea(path, time.Now())
}

func readKea(path string, now time.Time) ([]Lease, error) {
	latest := map[netip.Addr]Lease{}
	active := map[netip.Addr]bool{}

	for _, f := range []string{path + ".completed", path + ".1", path + ".2", path} {
		if err := readKeaFile(f, now, latest, active); err != nil {
			return nil, err
		}
	}

	out := make([]Lease, 0, len(latest))

	for a, l := range latest {
		if active[a] && l.Hostname != "" {
			out = append(out, l)
		}
	}

	slices.SortFunc(out, func(a, b Lease) int { return a.Address.Compare(b.Address) })

	return out, nil
}

func readKeaFile(path string, now time.Time, latest map[netip.Addr]Lease, active map[netip.Addr]bool) error {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}

	if err != nil {
		return err
	}

	defer f.Close() //nolint:errcheck

	r := csv.NewReader(f)
	r.FieldsPerRecord = -1
	r.LazyQuotes = true

	col := map[string]int{}

	for {
		rec, err := r.Read()
		if errors.Is(err, io.EOF) {
			return nil
		}

		if err != nil {
			continue // a line being written
		}

		if len(rec) > 0 && rec[0] == "address" {
			for i, name := range rec {
				col[name] = i
			}

			continue
		}

		get := func(name string) string {
			if i, ok := col[name]; ok && i < len(rec) {
				return rec[i]
			}

			return ""
		}

		addr, err := netip.ParseAddr(get("address"))
		if err != nil {
			continue
		}

		expire, _ := strconv.ParseInt(get("expire"), 10, 64)
		state := get("state")

		latest[addr] = Lease{Address: addr, Hostname: firstLabel(get("hostname"))}
		active[addr] = (state == "0" || state == "") && expire > now.Unix()
	}
}

// firstLabel returns the first label of a valid hostname, "" for anything else.
func firstLabel(name string) string {
	labels := strings.Split(strings.ToLower(strings.TrimSuffix(name, ".")), ".")
	for _, l := range labels {
		if !labelRe.MatchString(l) {
			return ""
		}
	}

	return labels[0]
}
