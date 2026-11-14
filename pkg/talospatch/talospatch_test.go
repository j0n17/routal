package talospatch_test

import (
	"os"
	"strings"
	"testing"

	"github.com/j0n17/routal/pkg/config"
	"github.com/j0n17/routal/pkg/env"
	"github.com/j0n17/routal/pkg/nft"
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

	out, err := talospatch.Generate(raw, cfg, nil)
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

	out, err := talospatch.Generate(raw, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}

	// the forwarded port must be opened in Talos' prerouting filter, which runs before DNAT
	// only for the family of the target: the port is not forwarded for the other one
	want := "name: routal-pf-nas-https\nportSelector:\n  ports:\n    - 443\n  protocol: tcp\ningress:\n  - subnet: 0.0.0.0/0\n---"
	if !strings.Contains(string(out), want) {
		t.Errorf("missing %q in:\n%s", want, out)
	}
}

func TestGenerateDeletesStaleRules(t *testing.T) {
	raw := []byte(`apiVersion: v1alpha1
kind: FirewallZoneConfig
name: lan
links: [lan0]
---
apiVersion: v1alpha1
kind: FirewallRuleConfig
name: dns
from: lan
to: self
protocol: udp
ports: [53]
`)

	cfg, err := config.Load(raw)
	if err != nil {
		t.Fatal(err)
	}

	current := []byte(`version: v1alpha1
machine: {}
---
apiVersion: v1alpha1
kind: NetworkRuleConfig
name: routal-dns
---
apiVersion: v1alpha1
kind: NetworkRuleConfig
name: routal-old-ssh
---
apiVersion: v1alpha1
kind: NetworkRuleConfig
name: apid-lan
`)

	out, err := talospatch.Generate(raw, cfg, current)
	if err != nil {
		t.Fatal(err)
	}

	s := string(out)

	if !strings.Contains(s, "kind: NetworkRuleConfig\nname: routal-old-ssh\n$patch: delete\n") {
		t.Errorf("stale rule not deleted:\n%s", s)
	}

	if strings.Contains(s, "name: apid-lan") || strings.Count(s, "$patch: delete") != 1 {
		t.Errorf("deleted a rule routald does not own or still needs:\n%s", s)
	}
}

func TestGenerateDeletesStaleRulesFromResource(t *testing.T) {
	raw := []byte("apiVersion: v1alpha1\nkind: FirewallZoneConfig\nname: lan\nlinks: [lan0]\n")

	cfg, err := config.Load(raw)
	if err != nil {
		t.Fatal(err)
	}

	// talosctl get mc -o yaml
	current := []byte(`node: 192.168.64.41
metadata:
    namespace: config
    type: MachineConfigs.config.talos.dev
    id: v1alpha1
spec: |
    version: v1alpha1
    ---
    apiVersion: v1alpha1
    kind: NetworkRuleConfig
    name: routal-gone
`)

	out, err := talospatch.Generate(raw, cfg, current)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(string(out), "name: routal-gone\n$patch: delete") {
		t.Errorf("stale rule not deleted:\n%s", out)
	}
}

func TestGenerateSecretsAndPD(t *testing.T) {
	cfg, err := config.LoadFile("../../examples/reference/router.yaml")
	if err != nil {
		t.Fatal(err)
	}

	raw, _ := os.ReadFile("../../examples/reference/router.yaml")

	out, err := talospatch.Generate(raw, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}

	s := string(out)

	for _, want := range []string{
		"  - content: |\n      # placeholder: the PPPoE password from the ISP\n      changeme\n    mountPath: /etc/routal/secrets/pppoe-isp.pass\n",
		"mountPath: /etc/routal/secrets/ddns-custom.pass",
		"name: routal-dhcpv6-client\nportSelector:\n  ports:\n    - 546\n  protocol: udp\ningress:\n  - subnet: fe80::/10\n",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in:\n%s", want, s)
		}
	}
}

func TestGenerateErrors(t *testing.T) {
	raw, _ := os.ReadFile("../../examples/reference/router.yaml")

	cfg, err := config.LoadFile("../../examples/reference/router.yaml")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := talospatch.Generate(raw, cfg, []byte("kind: [\n")); err == nil {
		t.Error("unreadable current machine config accepted")
	}

	// the secret files went away after the config was loaded
	cfg.BaseDir = t.TempDir()

	if _, err := talospatch.Generate(raw, cfg, nil); err == nil {
		t.Error("missing secret file accepted")
	}
}

// A forward whose sources are all of the other family forwards nothing: no port is opened.
func TestGeneratePortForwardFamily(t *testing.T) {
	raw := []byte("apiVersion: v1alpha1\nkind: PortForwardConfig\nname: web\ninLink: wan0\nprotocol: tcp\nports: [443]\n" +
		"toAddress: 192.168.1.10\nsourceSubnets: [\"2001:db8::/32\"]\n")

	cfg, err := config.Load(raw)
	if err != nil {
		t.Fatal(err)
	}

	out, err := talospatch.Generate(raw, cfg, nil)
	if err != nil || strings.Contains(string(out), "routal-pf-web") {
		t.Errorf("rule for a forward that matches nothing: %v\n%s", err, out)
	}

	if rules := nft.Render(cfg, env.Static()); strings.Contains(rules, "dnat ip to") {
		t.Errorf("DNAT for a forward that matches nothing:\n%s", rules)
	}
}
