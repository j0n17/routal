package talosrender_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/j0n17/routal/pkg/talosrender"
)

const key = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="

func TestRender(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "secrets"), 0o700)                                                                      //nolint:errcheck
	os.WriteFile(filepath.Join(dir, "secrets", "wg0.key"), []byte("# wg0\n"+key+"\n"), 0o600)                              //nolint:errcheck
	os.WriteFile(filepath.Join(dir, "secrets", "peer.psk"), []byte("BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBA="), 0o600) //nolint:errcheck

	out, err := talosrender.Render([]byte(`apiVersion: v1alpha1
kind: HostnameConfig
hostname: router
---
apiVersion: v1alpha1
kind: WireguardConfig
name: wg0
privateKeyFile: secrets/wg0.key
peers:
  - publicKey: CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCA=
    presharedKeyFile: secrets/peer.psk
`), dir)
	if err != nil {
		t.Fatal(err)
	}

	s := string(out)

	for _, want := range []string{
		"privateKey: " + key + " # from secrets/wg0.key",
		"presharedKey: BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBA= # from secrets/peer.psk",
		"hostname: router",
		"---\n",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in:\n%s", want, s)
		}
	}

	if strings.Contains(s, "KeyFile") {
		t.Errorf("unresolved reference:\n%s", s)
	}
}

func TestRenderErrors(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "bad.key"), []byte("not-a-key\n"), 0o600) //nolint:errcheck

	for name, doc := range map[string]string{
		"missing file": "privateKeyFile: nope.key\n",
		"not a key":    "privateKeyFile: bad.key\n",
	} {
		if _, err := talosrender.Render([]byte(doc), dir); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}
