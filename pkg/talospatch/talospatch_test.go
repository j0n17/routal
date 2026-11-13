package talospatch_test

import (
	"os"
	"strings"
	"testing"

	"github.com/j0n17/routal/pkg/config"
	"github.com/j0n17/routal/pkg/talospatch"
)

func TestGenerate(t *testing.T) {
	raw, err := os.ReadFile("../../examples/guest-and-vpn/router.yaml")
	if err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load(raw)
	if err != nil {
		t.Fatal(err)
	}

	out, err := talospatch.Generate(raw, cfg)
	if err != nil {
		t.Fatal(err)
	}

	s := string(out)

	for _, want := range []string{
		"kind: ExtensionServiceConfig\nname: routald\n",
		"mountPath: /etc/routal/router.yaml",
		"      kind: FirewallZoneConfig\n", // router.yaml embedded as a literal block
		"name: routal-wireguard\nportSelector:\n  ports:\n    - 51820\n  protocol: udp\n",
		"  - subnet: 0.0.0.0/0\n  - subnet: ::/0\n",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in:\n%s", want, s)
		}
	}

	if n := strings.Count(s, "kind: NetworkRuleConfig"); n != 3 {
		t.Errorf("NetworkRuleConfig count = %d, want 3", n)
	}
}

func TestGeneratePortForward(t *testing.T) {
	raw, err := os.ReadFile("../../examples/home-router/router.yaml")
	if err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load(raw)
	if err != nil {
		t.Fatal(err)
	}

	out, err := talospatch.Generate(raw, cfg)
	if err != nil {
		t.Fatal(err)
	}

	// the forwarded port must be opened in Talos' prerouting filter, which runs before DNAT
	want := "name: routal-pf-nas-https\nportSelector:\n  ports:\n    - 443\n  protocol: tcp\n"
	if !strings.Contains(string(out), want) {
		t.Errorf("missing %q in:\n%s", want, out)
	}
}
