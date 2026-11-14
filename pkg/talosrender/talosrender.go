// Package talosrender resolves file references in Talos documents, so that secrets stay out of git.
//
// Talos wants WireGuard keys inline in the machine config. In talos.yaml, write instead
//
//	privateKeyFile: secrets/wg0.key     # instead of privateKey
//	presharedKeyFile: secrets/peer.psk  # instead of presharedKey (peers)
//
// with paths relative to talos.yaml (or absolute). Render replaces each reference by the file's
// content; the key files themselves are kept out of git (or decrypted by CI, e.g. with SOPS).
package talosrender

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// fileFields maps a reference field to the Talos field it replaces.
var fileFields = map[string]string{
	"privateKeyFile":   "privateKey",
	"presharedKeyFile": "presharedKey",
}

// Render returns the documents with every file reference resolved; dir is the base of relative paths.
func Render(data []byte, dir string) ([]byte, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))

	var (
		out bytes.Buffer
		enc = yaml.NewEncoder(&out)
	)

	enc.SetIndent(2)

	for i := 1; ; i++ {
		var doc yaml.Node

		if err := dec.Decode(&doc); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}

			return nil, fmt.Errorf("document %d: %w", i, err)
		}

		if err := resolve(&doc, dir); err != nil {
			return nil, fmt.Errorf("document %d: %w", i, err)
		}

		if err := enc.Encode(&doc); err != nil {
			return nil, err
		}
	}

	if err := enc.Close(); err != nil {
		return nil, err
	}

	return out.Bytes(), nil
}

func resolve(n *yaml.Node, dir string) error {
	if n.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(n.Content); i += 2 {
			key, val := n.Content[i], n.Content[i+1]

			field, ok := fileFields[key.Value]
			if !ok {
				continue
			}

			secret, err := readKey(val.Value, dir)
			if err != nil {
				return fmt.Errorf("%s: %w", key.Value, err)
			}

			src := val.Value
			key.Value = field
			val.Value, val.Tag, val.Style = secret, "!!str", 0
			val.LineComment = "# from " + src
		}
	}

	for _, c := range n.Content {
		if err := resolve(c, dir); err != nil {
			return err
		}
	}

	return nil
}

// readKey reads a WireGuard key file (`wg genkey` output): one base64 key of 32 bytes; blank lines
// and # comments are ignored.
func readKey(path, dir string) (string, error) {
	if path == "" {
		return "", errors.New("empty path")
	}

	if !filepath.IsAbs(path) {
		path = filepath.Join(dir, path)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}

	var key string

	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		if key != "" {
			return "", fmt.Errorf("%s: more than one key", path)
		}

		key = line
	}

	if b, err := base64.StdEncoding.DecodeString(key); err != nil || len(b) != 32 {
		return "", fmt.Errorf("%s: not a WireGuard key (base64 of 32 bytes)", path)
	}

	return key, nil
}
