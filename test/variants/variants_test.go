// Package variants checks every config variant: the valid ones load and render without warnings
// (make check also runs them through nft, Kea, Unbound, radvd, chrony and inadyn), the invalid
// ones fail with the error in their "# error:" header.
package variants

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/j0n17/routal/pkg/config"
	"github.com/j0n17/routal/pkg/env"
	"github.com/j0n17/routal/pkg/nft"
	"github.com/j0n17/routal/pkg/services"
	"github.com/j0n17/routal/pkg/talospatch"
)

func TestValid(t *testing.T) {
	files, _ := filepath.Glob("*.yaml")
	if len(files) < 5 {
		t.Fatalf("variants not found: %v", files)
	}

	for _, f := range files {
		t.Run(f, func(t *testing.T) {
			cfg, err := config.LoadFile(f)
			if err != nil {
				t.Fatal(err)
			}

			if out := nft.Render(cfg, env.Static()); !strings.Contains(out, "table inet routal {") {
				t.Errorf("no ruleset:\n%s", out)
			}

			for _, s := range services.Render(cfg, env.Static(), services.DefaultDirs) {
				if len(s.Warnings) > 0 {
					t.Errorf("%s: %v", s.Name, s.Warnings)
				}
			}

			raw, _ := os.ReadFile(f)
			if _, err := talospatch.Generate(raw, cfg, nil); err != nil {
				t.Error(err)
			}
		})
	}
}

func TestInvalid(t *testing.T) {
	files, _ := filepath.Glob("invalid/*.yaml")
	if len(files) < 20 {
		t.Fatalf("invalid variants not found: %v", files)
	}

	for _, f := range files {
		t.Run(f, func(t *testing.T) {
			want := expectedError(t, f)

			_, err := config.LoadFile(f)
			if err == nil {
				t.Fatalf("accepted, want error %q", want)
			}

			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q does not contain %q", err, want)
			}
		})
	}
}

func expectedError(t *testing.T, path string) string {
	t.Helper()

	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close() //nolint:errcheck

	sc := bufio.NewScanner(f)
	sc.Scan()

	want, ok := strings.CutPrefix(sc.Text(), "# error: ")
	if !ok {
		t.Fatalf("first line must be \"# error: <expected message>\"")
	}

	return want
}
