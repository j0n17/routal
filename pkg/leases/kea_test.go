package leases

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestReadKea(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "kea-leases4.csv")
	now := time.Unix(1_800_000_000, 0)

	header := "address,hwaddr,client_id,valid_lifetime,expire,subnet_id,fqdn_fwd,fqdn_rev,hostname,state,user_context,pool_id\n"
	// rotated by LFC: the older file has a lease that the current file renews under a new name
	os.WriteFile(path+".1", []byte(header+
		"192.168.1.100,aa:bb:cc:dd:ee:01,,3600,1800000500,1,0,0,laptop,0,,0\n"), 0o644) //nolint:errcheck
	os.WriteFile(path, []byte(header+
		"192.168.1.100,aa:bb:cc:dd:ee:01,,3600,1800003000,1,0,0,Laptop2.,0,,0\n"+
		"192.168.1.101,aa:bb:cc:dd:ee:02,,3600,1799999000,1,0,0,expired,0,,0\n"+
		"192.168.1.102,aa:bb:cc:dd:ee:03,,3600,1800003000,1,0,0,evil. A 1.2.3.4,0,,0\n"+
		"192.168.1.103,aa:bb:cc:dd:ee:04,,3600,1800003000,1,0,0,declined,1,,0\n"+
		"192.168.1.104,aa:bb:cc:dd:ee:05,,3600,1800003000,1,0,0,tv.example.org,0,,0\n"+
		"192.168.1.105,aa:bb:cc:dd:ee:06,,3600,18000"), 0o644) //nolint:errcheck

	got, err := readKea(path, now)
	if err != nil {
		t.Fatal(err)
	}

	want := map[string]string{"192.168.1.100": "laptop2", "192.168.1.104": "tv"}

	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}

	for _, l := range got {
		if want[l.Address.String()] != l.Hostname {
			t.Errorf("got %+v", l)
		}
	}
}
