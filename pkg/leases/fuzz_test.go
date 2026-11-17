package leases

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// FuzzKea feeds arbitrary lease files (Kea writes them while they are read, and clients choose
// their hostname): reading ends, and every lease that comes out has an address and a hostname
// that is a plain DNS label.
func FuzzKea(f *testing.F) {
	header := "address,hwaddr,client_id,valid_lifetime,expire,subnet_id,fqdn_fwd,fqdn_rev,hostname,state,user_context,pool_id\n"
	f.Add([]byte(header + "192.168.1.100,52:54:00:00:00:10,,3600,4102444800,1,0,0,laptop,0,,0\n"))
	f.Add([]byte(header + "192.168.1.100,52:54:00:00:00:10,,3600,4102444800,1,0,0,Laptop.home.arpa.,0,,0\n" +
		"192.168.1.100,52:54:00:00:00:10,,0,1,1,0,0,laptop,0,,0\n192.168.1.101,aa,,3600,4102444800,1,0,0,\"a\nb\",2,,0\n"))
	f.Add([]byte("address,hostname\n192.168.1.5,x\n\"unterminated,3\n::1,v6\nnot-an-address,y\n"))

	now := time.Unix(1700000000, 0)

	f.Fuzz(func(t *testing.T, data []byte) {
		path := filepath.Join(t.TempDir(), "leases.csv")
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}

		got, err := readKea(path, now)
		if err != nil {
			return
		}

		for i, l := range got {
			if !l.Address.IsValid() || !labelRe.MatchString(l.Hostname) {
				t.Fatalf("lease %+v", l)
			}

			if i > 0 && got[i-1].Address.Compare(l.Address) >= 0 {
				t.Fatalf("leases not sorted or duplicated: %v then %v", got[i-1].Address, l.Address)
			}
		}
	})
}
