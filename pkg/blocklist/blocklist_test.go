package blocklist

import (
	"slices"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	got := Parse(strings.NewReader(`# hosts format
0.0.0.0 ads.example.com
127.0.0.1 tracker.example.net # trailing comment
127.0.0.1 localhost
::1 ip6-localhost
0.0.0.0 0.0.0.0
192.168.1.1 router.lan
# domain list
Telemetry.Example.ORG.
single
! AdBlock
||adserver.example.io^
||example.io/path^
||*.wild.example^
@@||allowed.example^
||opt.example^$third-party
[Adblock Plus 2.0]
bad_label-.example
`))

	want := []string{"ads.example.com", "tracker.example.net", "telemetry.example.org", "adserver.example.io"}
	if !slices.Equal(got, want) {
		t.Errorf("Parse = %v, want %v", got, want)
	}
}

func TestZones(t *testing.T) {
	block, allow := Zones(
		[]string{"ads.example.com", "x.ads.example.com", "tracker.example.net", "cdn.example.org"},
		[]string{"ok.ads.example.com", "cdn.example.org", "unrelated.example"},
	)

	if want := []string{"ads.example.com", "tracker.example.net"}; !slices.Equal(block, want) {
		t.Errorf("block = %v, want %v (subdomains folded, allowed removed)", block, want)
	}

	if want := []string{"ok.ads.example.com"}; !slices.Equal(allow, want) {
		t.Errorf("allow = %v, want %v (only names inside a blocked zone)", allow, want)
	}
}
