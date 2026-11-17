package blocklist

import (
	"bytes"
	"strings"
	"testing"
)

// FuzzParse feeds arbitrary lists: every name that comes out is one Unbound can take as a zone
// (normalized, no blank or quote), and Zones accepts the result.
func FuzzParse(f *testing.F) {
	f.Add([]byte("0.0.0.0 ads.example.com\n||tracker.example^\nexample.org # comment\n! adblock comment\n"))
	f.Add([]byte("example.com##.ad\n||a.example^$third-party\n127.0.0.1 localhost\n*.wild.example\n"))
	f.Add([]byte("\xff\xfe\x00ads.example\r\nADS.Example.\n"))

	f.Fuzz(func(t *testing.T, data []byte) {
		names := Parse(bytes.NewReader(data))

		for _, n := range names {
			if again, ok := Normalize(n); !ok || again != n {
				t.Fatalf("%q is not normalized (%q, %v)", n, again, ok)
			}

			if strings.ContainsAny(n, " \t\r\n\"'\\") {
				t.Fatalf("%q would break out of an Unbound zone line", n)
			}
		}

		block, allow := Zones(names, names)
		if len(block) > len(names) || len(allow) > len(names) {
			t.Fatalf("Zones grew the list: %d names, %d blocked, %d allowed", len(names), len(block), len(allow))
		}
	})
}
