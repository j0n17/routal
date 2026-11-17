package schema_test

import (
	"bytes"
	"encoding/json"
	"flag"
	"go/ast"
	"go/doc"
	"go/parser"
	"go/token"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/j0n17/routal/pkg/config"
	"github.com/j0n17/routal/pkg/schema"
	"github.com/j0n17/routal/pkg/schema/schematest"
)

var update = flag.Bool("update", false, "rewrite router.schema.json (make schema)")

// kinds are the documents of router.yaml, in the order of the README.
var kinds = []struct {
	name string
	doc  any
}{
	{"ForwardingConfig", config.Forwarding{}},
	{"FirewallConfig", config.Firewall{}},
	{"FirewallZoneConfig", config.FirewallZone{}},
	{"FirewallRuleConfig", config.FirewallRule{}},
	{"FirewallAddressGroupConfig", config.AddressGroup{}},
	{"FirewallPortGroupConfig", config.PortGroup{}},
	{"MasqueradeConfig", config.Masquerade{}},
	{"PortForwardConfig", config.PortForward{}},
	{"DHCPServerConfig", config.DHCPServer{}},
	{"DNSServerConfig", config.DNSServer{}},
	{"RouterAdvertisementConfig", config.RouterAdvertisement{}},
	{"NTPServerConfig", config.NTPServer{}},
	{"MDNSReflectorConfig", config.MDNSReflector{}},
	{"UPnPConfig", config.UPnP{}},
	{"PrefixDelegationConfig", config.PrefixDelegation{}},
	{"PPPoEConfig", config.PPPoE{}},
	{"DynamicDNSConfig", config.DynamicDNS{}},
}

type object = map[string]any

// scalars are the types with their own YAML form.
var scalars = map[reflect.Type]object{
	reflect.TypeFor[config.Action]():   {"enum": []string{string(config.ActionAccept), string(config.ActionBlock)}},
	reflect.TypeFor[config.Protocol](): {"enum": []string{"tcp", "udp", "icmp", "icmpv6"}},
	reflect.TypeFor[netip.Addr](): {
		"type": "string", "anyOf": []object{{"format": "ipv4"}, {"format": "ipv6"}},
	},
	reflect.TypeFor[netip.Prefix](): {
		"type": "string", "pattern": `^[0-9a-fA-F:.]+/[0-9]{1,3}$`,
	},
	reflect.TypeFor[time.Duration](): {
		"type": "string", "pattern": `^([0-9]+(\.[0-9]+)?(ns|us|µs|ms|s|m|h))+$`,
	},
	reflect.TypeFor[config.PortRange](): {
		"anyOf": []object{
			{"type": "integer", "minimum": 1, "maximum": 65535},
			{"type": "string", "pattern": `^\s*[0-9]+\s*(-\s*[0-9]+\s*)?$`},
		},
	},
}

// comments reads the doc comments of pkg/config: "Type" and "Type.Field".
func comments(t *testing.T) map[string]string {
	t.Helper()

	pkgs, err := parser.ParseDir(token.NewFileSet(), "../config", func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}

	out := map[string]string{}
	text := func(groups ...*ast.CommentGroup) string {
		for _, g := range groups {
			if s := strings.Join(strings.Fields(g.Text()), " "); s != "" {
				return s
			}
		}

		return ""
	}

	for _, typ := range doc.New(pkgs["config"], "config", 0).Types {
		out[typ.Name] = strings.Join(strings.Fields(typ.Doc), " ")

		st, ok := typ.Decl.Specs[0].(*ast.TypeSpec).Type.(*ast.StructType)
		if !ok {
			continue
		}

		for _, f := range st.Fields.List {
			for _, name := range f.Names {
				out[typ.Name+"."+name.Name] = text(f.Doc, f.Comment)
			}
		}
	}

	return out
}

// generator turns the config types into schema definitions.
type generator struct {
	docs map[string]string
	defs object
}

func (g *generator) typ(t reflect.Type) object {
	if s, ok := scalars[t]; ok {
		return s
	}

	switch t.Kind() {
	case reflect.Pointer:
		return g.typ(t.Elem())
	case reflect.Bool:
		return object{"type": "boolean"}
	case reflect.String:
		// YAML numbers and booleans decode into a string: `username: 12345` needs no quotes
		return object{"type": []string{"string", "number", "boolean"}}
	case reflect.Uint16:
		return object{"type": "integer", "minimum": 0, "maximum": 65535}
	case reflect.Int, reflect.Uint32:
		return object{"type": "integer", "minimum": 0}
	case reflect.Slice:
		return object{"type": "array", "items": g.typ(t.Elem())}
	case reflect.Struct:
		g.defs[t.Name()] = g.object(t, "")

		return object{"$ref": "#/definitions/" + t.Name()}
	default:
		panic("schema: no JSON schema for " + t.String())
	}
}

// object is the schema of a struct; kind is set for a document (it then has apiVersion and kind).
func (g *generator) object(t reflect.Type, kind string) object {
	props := object{}
	required := []string{}

	if kind != "" {
		props["apiVersion"] = object{"const": config.APIVersion}
		props["kind"] = object{"const": kind}
		required = append(required, "apiVersion", "kind")
	}

	for i := range t.NumField() {
		f := t.Field(i)

		name, opts, _ := strings.Cut(f.Tag.Get("yaml"), ",")
		if name == "-" {
			continue
		}

		prop := object{}
		for k, v := range g.typ(f.Type) {
			prop[k] = v
		}

		switch tag := f.Tag.Get("schema"); {
		case tag == "link":
			// a link may be called "true": YAML reads a boolean, routald a name
			link := object{"type": []string{"string", "boolean"}, "pattern": config.LinkPattern()}
			if f.Type.Kind() == reflect.Slice {
				prop["items"] = link
			} else {
				prop = link
			}
		case strings.HasPrefix(tag, "enum="):
			prop = object{"enum": strings.Split(strings.TrimPrefix(tag, "enum="), "|")}
		case tag != "":
			panic("schema: unknown tag " + tag)
		}

		if d := g.docs[t.Name()+"."+f.Name]; d != "" {
			prop["description"] = d
		}

		props[name] = prop

		if !strings.Contains(opts, "omitempty") {
			required = append(required, name)
		}
	}

	return object{
		"type": "object", "description": g.docs[t.Name()], "properties": props,
		"required": required, "additionalProperties": false,
	}
}

func generate(t *testing.T) []byte {
	t.Helper()

	g := generator{docs: comments(t), defs: object{}}

	var docs []object

	for _, k := range kinds {
		g.defs[k.name] = g.object(reflect.TypeOf(k.doc), k.name)
		docs = append(docs, object{"$ref": "#/definitions/" + k.name})
	}

	var out bytes.Buffer

	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")

	if err := enc.Encode(object{
		"$schema":     "http://json-schema.org/draft-07/schema#",
		"$id":         schema.URL,
		"title":       "Routal router.yaml",
		"description": "One document of router.yaml. Generated from pkg/config by make schema; do not edit.",
		"oneOf":       docs,
		"definitions": g.defs,
	}); err != nil {
		t.Fatal(err)
	}

	return out.Bytes()
}

// The committed schema is what the types generate now.
func TestUpToDate(t *testing.T) {
	want := generate(t)

	if *update {
		if err := os.WriteFile("router.schema.json", want, 0o644); err != nil { //nolint:gosec
			t.Fatal(err)
		}

		return
	}

	if !bytes.Equal(schema.JSON, want) {
		t.Fatal("router.schema.json is out of date: run make schema")
	}
}

// The schema has exactly the kinds the loader knows.
func TestKinds(t *testing.T) {
	src, err := os.ReadFile("../config/load.go")
	if err != nil {
		t.Fatal(err)
	}

	var loader, here []string

	for _, m := range regexp.MustCompile(`case "(\w+Config)":`).FindAllSubmatch(src, -1) {
		loader = append(loader, string(m[1]))
	}

	for _, k := range kinds {
		here = append(here, k.name)
	}

	slices.Sort(loader)
	slices.Sort(here)

	if !slices.Equal(loader, here) || len(here) == 0 {
		t.Fatalf("kinds of the loader: %v\nkinds of the schema: %v", loader, here)
	}
}

// Every example and valid variant matches the schema: it is never stricter than routald.
func TestValidConfigs(t *testing.T) {
	files, _ := filepath.Glob("../../examples/*/router.yaml")
	more, _ := filepath.Glob("../../test/variants/*.yaml")
	files = append(files, more...)

	if len(files) < 10 {
		t.Fatalf("configs not found: %v", files)
	}

	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}

		if err := schematest.Check(data); err != nil {
			t.Errorf("%s: %v", f, err)
		}
	}
}

// The editor underlines the mistakes a schema can see; the rest is routald validate's.
func TestInvalidConfigs(t *testing.T) {
	for _, name := range []string{
		"unknown-field", "forward-default", "link-name-long", "link-name-digit", "blocking-response", "api-version",
	} {
		data, err := os.ReadFile("../../test/variants/invalid/" + name + ".yaml")
		if err != nil {
			t.Fatal(err)
		}

		if schematest.Check(data) == nil {
			t.Errorf("%s matches the schema", name)
		}
	}

	files, _ := filepath.Glob("../../test/variants/invalid/*.yaml")
	caught := 0

	for _, f := range files {
		data, _ := os.ReadFile(f)
		if schematest.Check(data) != nil {
			caught++
		}
	}

	t.Logf("the schema alone refuses %d of the %d invalid variants", caught, len(files))
}
