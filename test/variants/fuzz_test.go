package variants

import (
	"bytes"
	"encoding/json"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/j0n17/routal/pkg/config"
	"github.com/j0n17/routal/pkg/env"
	"github.com/j0n17/routal/pkg/nft"
	"github.com/j0n17/routal/pkg/schema/schematest"
	"github.com/j0n17/routal/pkg/services"
	"github.com/j0n17/routal/pkg/talospatch"
	"gopkg.in/yaml.v3"
)

// FuzzConfig mutates the bytes of the examples and variants: whatever the input, loading does
// not panic, and a config that is accepted passes checkAccepted.
func FuzzConfig(f *testing.F) {
	secrets, tools := fuzzSecrets(f), fuzzTools()

	for _, data := range fuzzSeeds(f, true) {
		f.Add(data)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		checkAccepted(t, data, secrets, tools)
	})
}

// FuzzField keeps a valid config valid YAML and replaces one value in it: the fuzzer chooses the
// config, the field and the value. Random bytes rarely get past the YAML parser; this reaches the
// validation of every field, and what it lets through goes to the real tools.
func FuzzField(f *testing.F) {
	secrets, tools := fuzzSecrets(f), fuzzTools()
	seeds := fuzzSeeds(f, false)

	// values that end up in nftables rules, Unbound, Kea, radvd, chrony and inadyn configs
	for i, v := range []string{
		"", "0", "65535", "65536", "-1", "1-65535", "9-1", "0.0.0.0/0", "::/0", "10.0.0.300", "192.168.1.1/24",
		"fe80::1%eth0", "a b", "a\"b", "a\nb", "x; drop", "{ }", "$x", "*", "..", "a.", ".a", "tcp", "icmp", "self",
		"true", "null", "~", "1h", "1s", "99999h", "lan0.4095", "0123456789abcdef", "é", "\x00", "#", "'", "\\",
	} {
		for seed := range seeds {
			f.Add(uint8(seed), uint16(i*7), v)
		}
	}

	f.Fuzz(func(t *testing.T, seed uint8, field uint16, value string) {
		var (
			docs    []*yaml.Node
			scalars []*yaml.Node
		)

		dec := yaml.NewDecoder(bytes.NewReader(seeds[int(seed)%len(seeds)]))

		for {
			var doc yaml.Node
			if dec.Decode(&doc) != nil {
				break
			}

			docs = append(docs, &doc)
			scalars = fuzzValues(&doc, scalars)
		}

		// the plain scalar is typed by YAML again: 53 stays a number, "x" a string
		n := scalars[int(field)%len(scalars)]
		n.Value, n.Tag, n.Style = value, "", 0

		var out bytes.Buffer

		enc := yaml.NewEncoder(&out)
		for _, doc := range docs {
			if enc.Encode(doc) != nil {
				return
			}
		}

		checkAccepted(t, out.Bytes(), secrets, tools)
	})
}

// fuzzValues appends the scalar values under n (not the mapping keys).
func fuzzValues(n *yaml.Node, out []*yaml.Node) []*yaml.Node {
	switch n.Kind {
	case yaml.ScalarNode:
		return append(out, n)
	case yaml.MappingNode:
		for i := 1; i < len(n.Content); i += 2 {
			out = fuzzValues(n.Content[i], out)
		}
	default:
		for _, c := range n.Content {
			out = fuzzValues(c, out)
		}
	}

	return out
}

// live is the router with addresses on every link, as renderers see it once Talos configured them.
var live = env.Env{
	Resolve: func(n string) string { return n },
	Addrs: func(string) []netip.Prefix {
		return []netip.Prefix{
			netip.MustParsePrefix("192.0.2.1/24"), netip.MustParsePrefix("fe80::1/64"), netip.MustParsePrefix("2001:db8:1::1/64"),
		}
	},
}

// checkAccepted loads a config. When it is accepted, it matches the JSON schema, everything
// renders, twice the same, the Kea configs are JSON and the Talos patch is generated; with nft,
// unbound-checkconf, radvd and chronyd on the PATH (make fuzz runs in the daemons image) the real
// tools must accept what was rendered. A config that validation accepts and a tool refuses is a
// hole in validation or an injection into a generated config.
func checkAccepted(t *testing.T, data []byte, secrets string, tools map[string]string) {
	cfg, err := config.LoadDir(data, secrets)
	if err != nil {
		return
	}

	// the editor must not underline what routald accepts
	if err := schematest.Check(data); err != nil {
		t.Fatalf("accepted by routald, refused by the JSON schema: %v\n--- router.yaml\n%s", err, data)
	}

	for _, e := range []env.Env{env.Static(), live} {
		ruleset := nft.Render(cfg, e)
		if again := nft.Render(cfg, e); again != ruleset {
			t.Fatalf("ruleset differs between two renders:\n%s\n---\n%s", ruleset, again)
		}

		svcs := services.Render(cfg, e, services.DefaultDirs)
		again := services.Render(cfg, e, services.DefaultDirs)

		for i, s := range svcs {
			if again[i].Config != s.Config {
				t.Fatalf("%s differs between two renders:\n%s\n---\n%s", s.Name, s.Config, again[i].Config)
			}

			// Kea takes a comment line before the JSON
			_, body, _ := strings.Cut(s.Config, "\n")
			if strings.HasSuffix(s.File, ".json") && !json.Valid([]byte(body)) {
				t.Fatalf("%s is not JSON:\n%s", s.File, s.Config)
			}
		}

		if len(tools) == 0 {
			continue
		}

		dir := t.TempDir()
		check := func(what, content string, cmd ...string) {
			if tools[cmd[0]] == "" {
				return
			}

			path := filepath.Join(dir, what)
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}

			if out, err := exec.Command(tools[cmd[0]], append(cmd[1:], path)...).CombinedOutput(); err != nil {
				t.Fatalf("%s refuses %s: %v\n%s\n--- %s\n%s\n--- router.yaml\n%s", cmd[0], what, err, out, what, content, data)
			}
		}

		check("ruleset.nft", ruleset, "nft", "-c", "-f")

		for _, s := range svcs {
			switch s.File {
			case "unbound.conf":
				check(s.File, s.Config, "unbound-checkconf")
			case "radvd.conf":
				check(s.File, s.Config, "radvd", "-c", "-C")
			case "chrony.conf":
				check(s.File, s.Config, "chronyd", "-p", "-f")
			}
		}
	}

	if _, err := talospatch.Generate(data, cfg, nil); err != nil {
		t.Fatalf("talos patch: %v", err)
	}
}

// fuzzSeeds returns the examples and the valid variants, and the invalid variants when asked.
func fuzzSeeds(f *testing.F, invalid bool) [][]byte {
	files, _ := filepath.Glob("../../examples/*/router.yaml")
	more, _ := filepath.Glob("*.yaml")
	files = append(files, more...)

	if invalid {
		more, _ = filepath.Glob("invalid/*.yaml")
		files = append(files, more...)
	}

	if len(files) < 10 {
		f.Fatalf("seeds not found: %v", files)
	}

	var out [][]byte

	for _, s := range files {
		data, err := os.ReadFile(s)
		if err != nil {
			f.Fatal(err)
		}

		out = append(out, data)
	}

	return out
}

// fuzzTools finds the tools that check rendered configs; none outside the daemons image.
func fuzzTools() map[string]string {
	tools := map[string]string{}

	for _, name := range []string{"nft", "unbound-checkconf", "radvd", "chronyd"} {
		if path, err := exec.LookPath(name); err == nil {
			tools[name] = path
		}
	}

	return tools
}

// fuzzSecrets gathers the secret files of the examples and variants in one directory, so that
// mutated configs still find theirs.
func fuzzSecrets(f *testing.F) string {
	dir := f.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "secrets"), 0o700); err != nil {
		f.Fatal(err)
	}

	for _, pattern := range []string{"../../examples/*/secrets/*", "secrets/*", "invalid/secrets/*"} {
		files, _ := filepath.Glob(pattern)
		for _, src := range files {
			data, err := os.ReadFile(src)
			if err != nil {
				f.Fatal(err)
			}

			if err := os.WriteFile(filepath.Join(dir, "secrets", filepath.Base(src)), data, 0o600); err != nil {
				f.Fatal(err)
			}
		}
	}

	return dir
}
