package main

import (
	"bytes"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/text"
)

// repo is where links to files of the repository point, from the page.
const repo = "https://github.com/j0n17/routal/blob/main/"

var md = goldmark.New(goldmark.WithExtensions(extension.GFM))

// guide is a hand-written page of docs/, rendered as a section of the web page.
type guide struct {
	name  string // file name without .md: the section's id is guide-<name>
	title string // its first heading
	html  string
}

func slug(s string) string {
	s = strings.ToLower(s)
	s = regexp.MustCompile(`[^a-z0-9]+`).ReplaceAllString(s, "-")

	return strings.Trim(s, "-")
}

// renderMarkdown renders a file of the repository for the page: its headings move down by shift
// levels and get ids prefixed with prefix, links to other docs/*.md become links into the page, and
// links to other files of the repository go to GitHub. The first heading is returned apart when
// dropTitle is set.
func renderMarkdown(file string, shift int, prefix string, dropTitle bool) (title, out string, err error) {
	src, err := os.ReadFile(file)
	if err != nil {
		return "", "", err
	}

	doc := md.Parser().Parse(text.NewReader(src))
	dir := filepath.Dir(file)

	var drop []ast.Node

	err = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}

		switch n := n.(type) {
		case *ast.Heading:
			t := plain(n, src)
			if title == "" && dropTitle {
				title = t
				drop = append(drop, n)

				return ast.WalkSkipChildren, nil
			}

			n.Level = min(n.Level+shift, 6)
			n.SetAttributeString("id", []byte(prefix+slug(t)))
		case *ast.Link:
			n.Destination = []byte(link(dir, string(n.Destination)))
		}

		return ast.WalkContinue, nil
	})
	if err != nil {
		return "", "", err
	}

	for _, n := range drop {
		n.Parent().RemoveChild(n.Parent(), n)
	}

	var b bytes.Buffer
	if err := md.Renderer().Render(&b, src, doc); err != nil {
		return "", "", err
	}

	return title, alerts(b.String()), nil
}

// plain is the text of a heading.
func plain(n ast.Node, src []byte) string {
	var b strings.Builder

	_ = ast.Walk(n, func(c ast.Node, entering bool) (ast.WalkStatus, error) {
		if t, ok := c.(*ast.Text); ok && entering {
			b.Write(t.Segment.Value(src))
		}

		return ast.WalkContinue, nil
	})

	return b.String()
}

// link turns a link of a file in dir into one that works from the page.
func link(dir, dest string) string {
	if dest == "" || strings.HasPrefix(dest, "#") || strings.Contains(dest, "://") || strings.HasPrefix(dest, "mailto:") {
		return dest
	}

	target, frag, _ := strings.Cut(dest, "#")
	target = filepath.ToSlash(filepath.Join(dir, target))

	// another guide: its section in the page
	if path.Dir(target) == "docs" && strings.HasSuffix(target, ".md") {
		name := strings.TrimSuffix(path.Base(target), ".md")
		if frag != "" {
			return "#guide-" + name + "-" + frag
		}

		return "#guide-" + name
	}

	if frag != "" {
		frag = "#" + frag
	}

	if strings.HasSuffix(dest, "/") {
		return strings.Replace(repo, "/blob/", "/tree/", 1) + target + "/" + frag
	}

	return repo + target + frag
}

var alertRe = regexp.MustCompile(`(?s)<blockquote>\s*<p>\[!(NOTE|TIP|IMPORTANT|WARNING|CAUTION)\]\s*(.*?)</blockquote>`)

// alerts renders GitHub's alerts (> [!WARNING]) as the page's boxes.
func alerts(h string) string {
	return alertRe.ReplaceAllStringFunc(h, func(m string) string {
		s := alertRe.FindStringSubmatch(m)
		class := "note"
		if s[1] == "WARNING" || s[1] == "CAUTION" || s[1] == "IMPORTANT" {
			class = "warn"
		}

		return fmt.Sprintf(`<div class=%q><p>%s</div>`, class, s[2])
	})
}

// guides renders docs/*.md, in the order given, then any other file of docs/ by name.
func guides(order []string) ([]guide, error) {
	files, _ := filepath.Glob("docs/*.md")

	var names []string

	names = append(names, order...)

	for _, f := range files {
		n := strings.TrimSuffix(filepath.Base(f), ".md")
		if !slices.Contains(names, n) {
			names = append(names, n)
		}
	}

	var out []guide

	for _, n := range names {
		title, h, err := renderMarkdown(filepath.Join("docs", n+".md"), 1, "guide-"+n+"-", true)
		if err != nil {
			return nil, err
		}

		out = append(out, guide{name: n, title: title, html: h})
	}

	return out, nil
}

// kindNotes renders docs/kinds/<Kind>.md: hand-written text shown under a document's generated
// reference. A file for a kind the schema does not have is an error.
func kindNotes(known []string) (map[string]string, error) {
	files, _ := filepath.Glob("docs/kinds/*.md")
	notes := map[string]string{}

	for _, f := range files {
		kind := strings.TrimSuffix(filepath.Base(f), ".md")
		if !slices.Contains(known, kind) {
			return nil, fmt.Errorf("%s: no document or type %s in the schema", f, kind)
		}

		_, h, err := renderMarkdown(f, 3, kind+"-", false)
		if err != nil {
			return nil, err
		}

		notes[kind] = h
	}

	return notes, nil
}
