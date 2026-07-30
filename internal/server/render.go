package server

import (
	"bytes"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/sidharthpunathil/circe/web"

	chromahtml "github.com/alecthomas/chroma/v2/formatters/html"
	"github.com/yuin/goldmark"
	highlighting "github.com/yuin/goldmark-highlighting/v2"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
)

// md renders SKILL.md bodies. Raw HTML is deliberately left escaped: skills can
// be imported from third parties, and rendering their inline HTML would make
// this an XSS vector against the person browsing their own registry.
var md = goldmark.New(
	goldmark.WithExtensions(
		extension.GFM,
		extension.Typographer,
		highlighting.NewHighlighting(
			highlighting.WithStyle("github-dark"),
			// Keep chroma's <pre> wrapper — it is the scroll container for long
			// lines. Its inline background is overridden in CSS instead.
			highlighting.WithFormatOptions(chromahtml.WithClasses(false)),
		),
	),
	// Hard wraps are deliberately not enabled: SKILL.md bodies are ordinary
	// CommonMark, and soft-wrapped prose would render with ragged forced breaks.
	goldmark.WithParserOptions(parser.WithAutoHeadingID()),
)

func renderMarkdown(src string) template.HTML {
	var buf bytes.Buffer
	if err := md.Convert([]byte(src), &buf); err != nil {
		// Fall back to escaped plain text rather than showing nothing.
		return template.HTML("<pre>" + template.HTMLEscapeString(src) + "</pre>")
	}
	return template.HTML(buf.String())
}

// funcs are available to every template.
var funcs = template.FuncMap{
	"markdown": renderMarkdown,
	"bytes":    humanBytes,
	"ago":      humanTime,
	"pluralize": func(n int, one, many string) string {
		if n == 1 {
			return one
		}
		return many
	},
	"truncate": func(n int, s string) string {
		r := []rune(s)
		if len(r) <= n {
			return s
		}
		return strings.TrimRight(string(r[:n]), " ,.;:") + "…"
	},
	"join":  strings.Join,
	"lower": strings.ToLower,
	"dict": func(kv ...any) map[string]any {
		m := map[string]any{}
		for i := 0; i+1 < len(kv); i += 2 {
			if k, ok := kv[i].(string); ok {
				m[k] = kv[i+1]
			}
		}
		return m
	},
	"hasSuffix": strings.HasSuffix,
	"add":       func(a, b int) int { return a + b },
	"seq": func(n int) []int {
		out := make([]int, n)
		for i := range out {
			out[i] = i
		}
		return out
	},
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for x := n / unit; x >= unit; x /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGT"[exp])
}

func humanTime(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		n := int(d.Minutes())
		return fmt.Sprintf("%dm ago", n)
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	default:
		return t.Format("2 Jan 2006")
	}
}

// templates holds one parsed template set per page, each combining the layout,
// every partial, and that page.
type templates struct {
	pages map[string]*template.Template
	dev   bool
	dir   string // on-disk template dir when dev is set
}

func loadTemplates(dev bool, dir string) (*templates, error) {
	t := &templates{pages: map[string]*template.Template{}, dev: dev, dir: dir}
	return t, t.parse()
}

func (t *templates) parse() error {
	var srcFS fs.FS = web.Templates
	prefix := "templates"
	pageGlob := prefix + "/pages/*.html"
	var pageFiles []string
	var err error

	if t.dev {
		pageFiles, err = filepath.Glob(filepath.Join(t.dir, "pages", "*.html"))
		if err != nil {
			return err
		}
	} else {
		pageFiles, err = fs.Glob(srcFS, pageGlob)
		if err != nil {
			return err
		}
	}
	if len(pageFiles) == 0 {
		return fmt.Errorf("no page templates found")
	}

	pages := map[string]*template.Template{}
	for _, page := range pageFiles {
		name := strings.TrimSuffix(filepath.Base(page), ".html")
		tmpl := template.New("layout.html").Funcs(funcs)

		var parsed *template.Template
		if t.dev {
			files := []string{filepath.Join(t.dir, "layout.html")}
			partials, _ := filepath.Glob(filepath.Join(t.dir, "partials", "*.html"))
			files = append(files, partials...)
			files = append(files, page)
			parsed, err = tmpl.ParseFiles(files...)
		} else {
			parsed, err = tmpl.ParseFS(srcFS,
				prefix+"/layout.html",
				prefix+"/partials/*.html",
				page,
			)
		}
		if err != nil {
			return fmt.Errorf("parse %s: %w", name, err)
		}
		pages[name] = parsed
	}
	t.pages = pages
	return nil
}

// render writes a page. In dev mode templates are re-parsed on every request so
// edits show up without a restart.
func (t *templates) render(w http.ResponseWriter, r *http.Request, status int, page string, data any) {
	if t.dev {
		if err := t.parse(); err != nil {
			http.Error(w, "template error: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}
	tmpl, ok := t.pages[page]
	if !ok {
		http.Error(w, "unknown page: "+page, http.StatusInternalServerError)
		return
	}

	// Render into a buffer first so a template error cannot emit a half-written
	// page on top of an already-sent 200.
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "layout.html", data); err != nil {
		http.Error(w, "render error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	buf.WriteTo(w)
}
