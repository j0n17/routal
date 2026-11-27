// Command site generates the project's web page (make site, published by GitHub Pages): the
// reference of every router.yaml document from the JSON schema, the example routers and the
// personas the VM test plays, each with its files.
//
//	go run ./hack/site <out dir>
package main

import (
	"encoding/json"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

type schemaDoc struct {
	OneOf []struct {
		Ref string `json:"$ref"`
	} `json:"oneOf"`
	Definitions map[string]definition `json:"definitions"`
}

type definition struct {
	Description string              `json:"description"`
	Properties  map[string]property `json:"properties"`
	Required    []string            `json:"required"`
}

type property struct {
	Description string          `json:"description"`
	Type        json.RawMessage `json:"type"`
	Ref         string          `json:"$ref"`
	Const       any             `json:"const"`
	Enum        []any           `json:"enum"`
	Items       *property       `json:"items"`
	OneOf       []property      `json:"oneOf"`
	AnyOf       []property      `json:"anyOf"`
}

func refName(ref string) string { return ref[strings.LastIndex(ref, "/")+1:] }

// typeOf describes a property the way a user writes it.
func typeOf(p property) string {
	switch {
	case p.Ref != "":
		n := refName(p.Ref)

		return fmt.Sprintf(`<a href="#%s">%s</a>`, n, n)
	case len(p.Enum) > 0:
		vals := make([]string, len(p.Enum))
		for i, v := range p.Enum {
			vals[i] = html.EscapeString(fmt.Sprint(v))
		}

		return strings.Join(vals, " | ")
	case p.Items != nil:
		return "list of " + typeOf(*p.Items)
	case len(p.OneOf) > 0:
		return typeOf(p.OneOf[0])
	case len(p.AnyOf) > 0:
		return typeOf(p.AnyOf[0])
	}

	var one string
	if json.Unmarshal(p.Type, &one) == nil {
		return one
	}

	var many []string
	if json.Unmarshal(p.Type, &many) == nil && len(many) > 0 {
		return many[0] // the first is the natural one; the others are what YAML may parse it as
	}

	return ""
}

var (
	yamlComment = regexp.MustCompile(`^(\s*)(#.*)$`)
	yamlKey     = regexp.MustCompile(`^(\s*(?:- )?)([A-Za-z0-9_$.-]+)(:)(.*)$`)
	trailing    = regexp.MustCompile(`^(.*?)(\s+#.*)$`)
)

// highlight colors a YAML file: comments, keys, document separators.
func highlight(src string) string {
	var b strings.Builder

	for _, line := range strings.Split(strings.TrimRight(src, "\n"), "\n") {
		switch {
		case line == "---":
			b.WriteString(`<span class="sep">---</span>`)
		case yamlComment.MatchString(line):
			m := yamlComment.FindStringSubmatch(line)
			fmt.Fprintf(&b, `%s<span class="c">%s</span>`, m[1], html.EscapeString(m[2]))
		case yamlKey.MatchString(line):
			m := yamlKey.FindStringSubmatch(line)
			rest, comment := m[4], ""

			if t := trailing.FindStringSubmatch(rest); t != nil && !strings.Contains(t[1], `"`) {
				rest, comment = t[1], t[2]
			}

			fmt.Fprintf(&b, `%s<span class="k">%s</span>%s%s`, m[1], html.EscapeString(m[2]), m[3], html.EscapeString(rest))

			if comment != "" {
				fmt.Fprintf(&b, `<span class="c">%s</span>`, html.EscapeString(comment))
			}
		default:
			b.WriteString(html.EscapeString(line))
		}

		b.WriteByte('\n')
	}

	return b.String()
}

// lead returns the comment block that opens a file, as text.
func lead(src string) string {
	var out []string

	for _, line := range strings.Split(src, "\n") {
		if !strings.HasPrefix(line, "#") {
			break
		}

		if strings.HasPrefix(line, "# yaml-language-server") || strings.HasPrefix(line, "# shellcheck") {
			continue
		}

		out = append(out, strings.TrimSpace(strings.TrimPrefix(line, "#")))
	}

	return strings.Join(out, " ")
}

func file(b *strings.Builder, title, path string, open bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return
	}

	attr := ""
	if open {
		attr = " open"
	}

	fmt.Fprintf(b, "<details%s><summary><code>%s</code> <span class=\"muted\">%d lines</span></summary><pre><code>%s</code></pre></details>\n",
		attr, html.EscapeString(title), strings.Count(string(raw), "\n"), highlight(string(raw)))
}

// expectations lists what a persona's checks assert: the messages of its pass lines.
func expectations(path string) []string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}

	var out []string

	for _, m := range regexp.MustCompile(`pass "([^"]+)"`).FindAllStringSubmatch(string(raw), -1) {
		text := regexp.MustCompile(`:? ?(->)? ?\$\{?[a-z_]+\}?`).ReplaceAllString(m[1], "")
		text = strings.TrimRight(strings.TrimSpace(text), ":")

		if text != "" && !slices.Contains(out, text) {
			out = append(out, text)
		}
	}

	return out
}

// referenceValues reads the reference example: for every kind, the fields of its first document
// as YAML text, to fill the templates with values that mean something.
func referenceValues() map[string]map[string]string {
	out := map[string]map[string]string{}

	raw, err := os.ReadFile("examples/reference/router.yaml")
	if err != nil {
		return out
	}

	dec := yaml.NewDecoder(strings.NewReader(string(raw)))

	for {
		var doc yaml.Node
		if dec.Decode(&doc) != nil {
			break
		}

		if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
			continue
		}

		m := doc.Content[0]
		kind, fields := "", map[string]string{}

		for i := 0; i+1 < len(m.Content); i += 2 {
			key, val := m.Content[i].Value, m.Content[i+1]
			if key == "kind" {
				kind = val.Value
			}

			stripComments(val)

			var b strings.Builder

			enc := yaml.NewEncoder(&b)
			enc.SetIndent(2)
			_ = enc.Encode(val)
			_ = enc.Close()

			fields[key] = strings.TrimRight(b.String(), "\n")
		}

		if _, seen := out[kind]; !seen {
			out[kind] = fields
		}
	}

	return out
}

func stripComments(n *yaml.Node) {
	n.HeadComment, n.LineComment, n.FootComment = "", "", ""
	for _, c := range n.Content {
		stripComments(c)
	}
}

// placeholder is the value of a field the reference example does not set.
func placeholder(p property) string {
	switch t := typeOf(p); {
	case strings.HasPrefix(t, "list of"):
		return "[]"
	case t == "boolean":
		return "false"
	case t == "integer" || t == "number":
		return "0"
	case p.Ref != "":
		return "{}"
	case len(p.Enum) > 0:
		return fmt.Sprint(p.Enum[0])
	default:
		return `""`
	}
}

// template is a document ready to paste: required fields as they are, the others commented out
// and marked optional, each with what it means.
func template(kind string, def definition, fields []string, values map[string]string) string {
	var b strings.Builder

	fmt.Fprintf(&b, "apiVersion: v1alpha1\nkind: %s\n", kind)

	for _, f := range fields {
		p := def.Properties[f]
		required := slices.Contains(def.Required, f)

		val, ok := values[f]
		if !ok {
			val = placeholder(p)
		}

		entry := f + ": " + val
		if strings.Contains(val, "\n") || strings.HasPrefix(val, "- ") {
			entry = f + ":\n  " + strings.ReplaceAll(val, "\n", "\n  ")
		}

		note := strings.Join(strings.Fields(p.Description), " ")
		if !required {
			note = strings.TrimSpace("optional: " + note)
			entry = "# " + strings.ReplaceAll(entry, "\n", "\n# ")
		}

		if note != "" {
			fmt.Fprintf(&b, "# %s\n", note)
		}

		b.WriteString(entry + "\n")
	}

	return b.String()
}

const script = `
document.addEventListener('click', function (e) {
  var b = e.target.closest('button.copy');
  if (!b) return;
  var text = document.getElementById(b.dataset.from).textContent;
  navigator.clipboard.writeText(text).then(function () {
    var old = b.textContent;
    b.textContent = 'Copied';
    setTimeout(function () { b.textContent = old; }, 1500);
  });
});
`

const style = `
:root { --bg:#fff; --fg:#1b1f24; --muted:#656d76; --line:#d8dee4; --code:#f6f8fa; --key:#0550ae; --accent:#0969da; --warn:#fff8c5; }
@media (prefers-color-scheme: dark) {
  :root { --bg:#0d1117; --fg:#e6edf3; --muted:#8b949e; --line:#30363d; --code:#161b22; --key:#79c0ff; --accent:#58a6ff; --warn:#3b2f00; }
}
* { box-sizing: border-box; }
body { margin:0; font:16px/1.55 -apple-system,BlinkMacSystemFont,"Segoe UI",Helvetica,Arial,sans-serif; color:var(--fg); background:var(--bg); }
a { color:var(--accent); text-decoration:none; } a:hover { text-decoration:underline; }
.layout { display:grid; grid-template-columns:260px minmax(0,1fr); max-width:1280px; margin:0 auto; }
nav { position:sticky; top:0; align-self:start; height:100vh; overflow:auto; padding:24px 16px; border-right:1px solid var(--line); font-size:14px; }
nav h1 { font-size:20px; margin:0 0 4px; } nav .logo { display:block; margin:0 0 10px; } nav h1 a { display:inline; } nav h2 { font-size:12px; text-transform:uppercase; letter-spacing:.06em; color:var(--muted); margin:20px 0 6px; }
nav a { display:block; padding:2px 0; color:var(--fg); } nav a:hover { color:var(--accent); }
main { padding:32px 40px 80px; min-width:0; }
h1 { font-size:32px; margin:0 0 8px; } h2 { font-size:24px; margin:48px 0 8px; padding-top:16px; border-top:1px solid var(--line); }
h3 { font-size:18px; margin:32px 0 6px; } h3 code { font-size:17px; }
p { margin:8px 0; } .muted { color:var(--muted); font-size:14px; font-weight:normal; }
.warn { background:var(--warn); border:1px solid var(--line); border-radius:6px; padding:12px 16px; margin:16px 0; }
table { border-collapse:collapse; width:100%; margin:8px 0 16px; font-size:14px; }
th,td { text-align:left; vertical-align:top; padding:6px 10px; border-bottom:1px solid var(--line); }
th { color:var(--muted); font-weight:600; } td:first-child { white-space:nowrap; } td:nth-child(2) { white-space:nowrap; color:var(--muted); }
code { font:13px/1.5 ui-monospace,SFMono-Regular,Menlo,Consolas,monospace; background:var(--code); padding:1px 5px; border-radius:4px; }
pre { background:var(--code); border:1px solid var(--line); border-radius:6px; padding:12px 14px; overflow:auto; margin:6px 0 14px; }
pre code { background:none; padding:0; }
.k { color:var(--key); } .c { color:var(--muted); font-style:italic; } .sep { color:var(--muted); }
details { margin:6px 0; } summary { cursor:pointer; padding:4px 0; }
ul { margin:6px 0 12px; padding-left:22px; } li { margin:2px 0; }
.req { color:#cf222e; font-size:12px; margin-left:4px; }
.anchor { margin-left:8px; color:var(--muted); font-weight:normal; opacity:0; } h2:hover .anchor, h3:hover .anchor, .anchor:focus { opacity:1; }
button.copy { font:13px/1 inherit; color:var(--fg); background:var(--code); border:1px solid var(--line); border-radius:6px; padding:6px 10px; margin-left:10px; cursor:pointer; vertical-align:middle; }
button.copy:hover { border-color:var(--accent); color:var(--accent); }
@media (max-width: 860px) { .layout { display:block; } nav { position:static; height:auto; border-right:0; border-bottom:1px solid var(--line); } main { padding:24px 18px 60px; } td:first-child, td:nth-child(2) { white-space:normal; } }
`

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: site <out dir>")
		os.Exit(2)
	}

	out := os.Args[1]

	raw, err := os.ReadFile("pkg/schema/router.schema.json")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	var sc schemaDoc
	if err := json.Unmarshal(raw, &sc); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	// documents first, in the order of the schema; then the types they are made of
	var kinds, helpers []string
	for _, ref := range sc.OneOf {
		kinds = append(kinds, refName(ref.Ref))
	}

	for name := range sc.Definitions {
		if !slices.Contains(kinds, name) {
			helpers = append(helpers, name)
		}
	}

	sort.Strings(helpers)

	examples, _ := filepath.Glob("examples/*/router.yaml")
	personas, _ := filepath.Glob("test/vm/personas/*/router.yaml")

	var nav, body strings.Builder

	nav.WriteString(`<a href="#top"><img class="logo" src="logo.svg" alt="Routal" width="180"></a>`)
	nav.WriteString(`<div class="muted">a router on stock Talos Linux</div>`)
	nav.WriteString(`<h2>Start</h2><a href="#top">What it is</a><a href="#layout">A router in git</a><a href="#apply">Apply and check</a>`)

	body.WriteString(`<h1 id="top">Routal</h1>
<p>A GitOps-managed router built on <a href="https://www.talos.dev">Talos Linux</a>: stock Talos runs the machine, and the
<code>routald</code> system extension turns it into a router: zone firewall, NAT, port forwards, UPnP, DHCP, DNS with
blocklists, IPv6 router advertisements and prefix delegation, PPPoE, NTP, mDNS.</p>
<div class="warn"><strong>A toy experiment more than a product.</strong> Routal is AI-generated code, written to try out
an idea. It has only been tested in VMs and network namespaces, never on real hardware or with a real ISP. It is
maintained, at its own pace: there is no release schedule and no promise of support. Do not put it in front of anything
you care about.</div>
<p>Source, tests and design notes: <a href="https://github.com/j0n17/routal">github.com/j0n17/routal</a>.</p>

<h2 id="layout">A router in git</h2>
<p>A router is a directory with two files. <code>talos.yaml</code> holds plain Talos documents: links, addresses, VLANs,
WireGuard, the router's own ingress firewall. <code>router.yaml</code> holds the documents on this page, read by
<code>routald</code>: what is forwarded, translated and served. Both are lists of YAML documents, each with a
<code>kind</code>; unknown fields are errors.</p>
<p>Editors can check <code>router.yaml</code> as you type with the <a href="router.schema.json">JSON schema</a>:</p>
<pre><code><span class="c"># yaml-language-server: $schema=https://j0n17.github.io/routal/router.schema.json</span></code></pre>

<h2 id="apply">Apply and check</h2>
<pre><code>routald validate router.yaml            <span class="c"># offline: every document and field</span>
routald render router.yaml              <span class="c"># the nftables ruleset routald would load</span>
bin/routal-apply router.yaml            <span class="c"># Talos reverts unless the router is healthy with it</span>
talosctl read /run/routal/routal/status.json | routald status -   <span class="c"># health: the config and every daemon</span></code></pre>
`)

	section := func(id, title, intro string) {
		fmt.Fprintf(&nav, `<h2>%s</h2>`, title)
		fmt.Fprintf(&body, "<h2 id=%q>%s<a class=\"anchor\" href=\"#%s\" aria-label=\"Link to this section\">#</a></h2>\n<p>%s</p>\n", id, title, id, intro)
	}

	values := referenceValues()

	define := func(name string, document bool) {
		def := sc.Definitions[name]

		copyButton := ""
		if document {
			copyButton = fmt.Sprintf(`<button class="copy" data-from="tpl-%s" title="Copy a %s to the clipboard: required fields set, optional ones commented out">Copy YAML</button>`, name, name)
		}

		fmt.Fprintf(&nav, `<a href="#%s">%s</a>`, name, name)
		fmt.Fprintf(&body, "<h3 id=%q><code>%s</code><a class=\"anchor\" href=\"#%s\" aria-label=\"Link to %s\">#</a>%s</h3>\n<p>%s</p>\n",
			name, name, name, name, copyButton, html.EscapeString(def.Description))

		var fields []string
		for f := range def.Properties {
			if f != "apiVersion" && f != "kind" {
				fields = append(fields, f)
			}
		}

		// the name first, then the required fields, then by name
		sort.Slice(fields, func(i, j int) bool {
			if (fields[i] == "name") != (fields[j] == "name") {
				return fields[i] == "name"
			}

			ri, rj := slices.Contains(def.Required, fields[i]), slices.Contains(def.Required, fields[j])
			if ri != rj {
				return ri
			}

			return fields[i] < fields[j]
		})

		if document {
			tpl := template(name, def, fields, values[name])
			fmt.Fprintf(&body, "<details><summary>YAML template <span class=\"muted\">required fields set, optional ones commented out</span></summary><pre><code id=\"tpl-%s\">%s</code></pre></details>\n",
				name, html.EscapeString(tpl))
		}

		if len(fields) == 0 {
			return
		}

		body.WriteString("<table><tr><th>Field</th><th>Type</th><th>Meaning</th></tr>\n")

		for _, f := range fields {
			p := def.Properties[f]

			req := ""
			if slices.Contains(def.Required, f) {
				req = `<span class="req">required</span>`
			}

			fmt.Fprintf(&body, "<tr><td><code>%s</code>%s</td><td>%s</td><td>%s</td></tr>\n", f, req, typeOf(p), html.EscapeString(p.Description))
		}

		body.WriteString("</table>\n")
	}

	section("documents", "Documents", "Every <code>kind</code> of <code>router.yaml</code>, with <code>apiVersion: v1alpha1</code>. Generated from the schema, which is generated from the code.")

	for _, k := range kinds {
		define(k, true)
	}

	section("types", "Nested types", "What some fields of the documents above are made of.")

	for _, k := range helpers {
		define(k, false)
	}

	section("examples", "Examples", "Complete routers from the repository. Each is validated by the tests with the real tools: nftables, Kea, Unbound, radvd, chrony, talosctl.")

	for _, path := range examples {
		dir := filepath.Dir(path)
		name := filepath.Base(dir)
		src, _ := os.ReadFile(path)

		fmt.Fprintf(&nav, `<a href="#example-%s">%s</a>`, name, name)
		fmt.Fprintf(&body, "<h3 id=\"example-%[1]s\"><code>%[1]s</code><a class=\"anchor\" href=\"#example-%[1]s\" aria-label=\"Link to this example\">#</a></h3>\n<p>%[2]s</p>\n", name, html.EscapeString(lead(string(src))))
		file(&body, "router.yaml", path, name == "home-router")
		file(&body, "talos.yaml", filepath.Join(dir, "talos.yaml"), false)
	}

	section("personas", "Personas", "Who uses such a router, and what each of them expects. The tests boot a real Talos in a VM, apply each of these configs, and check every line below from a device behind the router, a visitor on a VLAN, a laptop on the road and the Internet side.")

	for _, path := range personas {
		dir := filepath.Dir(path)
		name := regexp.MustCompile(`^\d+-`).ReplaceAllString(filepath.Base(dir), "")
		src, _ := os.ReadFile(path)

		fmt.Fprintf(&nav, `<a href="#persona-%s">%s</a>`, name, name)
		fmt.Fprintf(&body, "<h3 id=\"persona-%[1]s\"><code>%[1]s</code><a class=\"anchor\" href=\"#persona-%[1]s\" aria-label=\"Link to this persona\">#</a></h3>\n<p>%[2]s</p>\n", name, html.EscapeString(lead(string(src))))

		if exp := expectations(filepath.Join(dir, "checks.sh")); len(exp) > 0 {
			body.WriteString("<ul>\n")

			for _, e := range exp {
				fmt.Fprintf(&body, "<li>%s</li>\n", html.EscapeString(e))
			}

			body.WriteString("</ul>\n")
		}

		file(&body, "router.yaml", path, false)
	}

	page := fmt.Sprintf(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>Routal: a router on stock Talos Linux</title>
<meta name="description" content="Reference of router.yaml, example routers and tested personas of Routal, a router built on Talos Linux.">
<link rel="icon" type="image/svg+xml" href="icon.svg">
<style>%s</style></head>
<body><div class="layout"><nav>%s</nav><main>%s</main></div><script>%s</script></body></html>
`, style, nav.String(), body.String(), script)

	if err := os.MkdirAll(out, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	logo, err := os.ReadFile("assets/logo.svg")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	icon, err := os.ReadFile("assets/icon.svg")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	for name, content := range map[string][]byte{
		"index.html": []byte(page), "router.schema.json": raw, "logo.svg": logo, "icon.svg": icon, ".nojekyll": nil,
	} {
		if err := os.WriteFile(filepath.Join(out, name), content, 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}

	fmt.Printf("%s/index.html: %d documents, %d nested types, %d examples, %d personas\n", out, len(kinds), len(helpers), len(examples), len(personas))
}
