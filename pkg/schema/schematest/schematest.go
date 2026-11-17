// Package schematest validates router.yaml documents against the JSON schema, in tests.
package schematest

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"

	"github.com/j0n17/routal/pkg/schema"
)

var compiled = sync.OnceValues(func() (*jsonschema.Schema, error) {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schema.JSON))
	if err != nil {
		return nil, err
	}

	c := jsonschema.NewCompiler()
	c.AssertFormat()

	if err := c.AddResource(schema.URL, doc); err != nil {
		return nil, err
	}

	return c.Compile(schema.URL)
})

// Check validates every YAML document of data (empty documents are skipped, as routald does).
func Check(data []byte) error {
	sch, err := compiled()
	if err != nil {
		return err
	}

	dec := yaml.NewDecoder(bytes.NewReader(data))

	for i := 1; ; i++ {
		var doc any

		if err := dec.Decode(&doc); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}

			return fmt.Errorf("document %d: %w", i, err)
		}

		if m, ok := doc.(map[string]any); doc == nil || (ok && len(m) == 0) {
			continue
		}

		doc = asRoutald(doc)

		// through JSON: the validator wants JSON types (and a YAML map with other keys is not JSON)
		raw, err := json.Marshal(doc)
		if err != nil {
			return fmt.Errorf("document %d: %w", i, err)
		}

		value, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
		if err != nil {
			return fmt.Errorf("document %d: %w", i, err)
		}

		if err := sch.Validate(value); err != nil {
			return fmt.Errorf("document %d: %w", i, err)
		}
	}
}

// yaml11Bool are the YAML 1.1 booleans the decoder still takes for a boolean field. The schema
// wants true or false: this is the one place where an editor is stricter than routald.
var yaml11Bool = map[string]bool{
	"y": true, "Y": true, "yes": true, "Yes": true, "YES": true, "on": true, "On": true, "ON": true,
	"n": false, "N": false, "no": false, "No": false, "NO": false, "off": false, "Off": false, "OFF": false,
}

// asRoutald makes a decoded document what routald reads: a null or empty value is an absent field
// (`action: ""` is the default action), a float is truncated, and a YAML 1.1 boolean is a boolean
// (every string of the schema also takes one).
func asRoutald(v any) any {
	switch v := v.(type) {
	case map[string]any:
		for k, e := range v {
			if e == nil || e == "" {
				delete(v, k)
			} else {
				v[k] = asRoutald(e)
			}
		}
	case []any:
		for i, e := range v {
			v[i] = asRoutald(e)
		}
	case float64:
		// the decoder truncates a float for an integer field (`mtu: 1492.5`); the schema wants an integer
		if t := math.Trunc(v); !math.IsNaN(v) && !math.IsInf(v, 0) {
			return t
		}

		return 0
	case string:
		if b, ok := yaml11Bool[v]; ok {
			return b
		}
	}

	return v
}
