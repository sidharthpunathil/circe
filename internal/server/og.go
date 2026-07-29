package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"io/fs"
	"net/http"
	"os"
	"path"
	"strings"
	"sync"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gomedium"
	"golang.org/x/image/font/gofont/gomono"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
	"golang.org/x/image/vector"

	"github.com/sidharthp/skillbuilder/internal/store"
	"github.com/sidharthp/skillbuilder/web"
)

// Open Graph card rendering.
//
// Why PNG and not SVG: Facebook, LinkedIn, Slack, Discord and X all refuse an
// SVG og:image, so an SVG card would simply not appear anywhere it matters.
// Rasterising is therefore worth the cost.
//
// Why the Go fonts and not Inter: the site self-hosts Inter and IBM Plex Mono as
// woff2 only. woff2 is Brotli-compressed with a transformed glyf table, and Go
// has no stdlib Brotli — decoding it would mean a second dependency plus a font
// transform implementation. golang.org/x/image already ships the Go typefaces as
// plain Go source, so they cost no new binary asset and no build step. Go Medium
// is a humanist sans with a large x-height, which reads close enough to Inter at
// card sizes. To use the real thing, drop TTF/OTF files at the paths in
// ogFontOverrides (they are picked up automatically, embedded or from -dev disk).

const (
	ogWidth  = 1200
	ogHeight = 630

	// ogMaxAge is deliberately long: scrapers refetch rarely and the ETag below
	// still lets a conditional request notice an edit.
	ogMaxAge = 86400
)

// Palette lifted from web/css/input.css so the card and the site cannot drift.
var (
	ogBG     = color.RGBA{0x11, 0x11, 0x10, 0xff} // --color-bg
	ogCardBG = color.RGBA{0x0e, 0x0e, 0x0c, 0xff} // --color-card
	ogLine   = color.RGBA{0x22, 0x22, 0x21, 0xff} // --color-line
	ogFG     = color.RGBA{0xff, 0xff, 0xff, 0xff} // --color-fg
	ogMuted  = color.RGBA{0x8b, 0x8a, 0x83, 0xff} // --color-fg-3
	ogFaint  = color.RGBA{0x6f, 0x6d, 0x66, 0xff} // --color-fg-4
	ogAccent = color.RGBA{0xfa, 0xd6, 0x57, 0xff} // --color-accent
)

// ogFontOverrides are looked up before falling back to the built-in Go fonts, so
// a deployment can ship its own typeface without touching this file.
var ogFontOverrides = struct{ display, text, mono string }{
	display: "static/fonts/og-display.ttf",
	text:    "static/fonts/og-text.ttf",
	mono:    "static/fonts/og-mono.ttf",
}

// ---------- fonts ----------

type ogFonts struct {
	display *opentype.Font // headings and wordmark
	text    *opentype.Font // description
	mono    *opentype.Font // eyebrow and footer meta, echoing the site's .meta
}

var (
	ogFontsOnce sync.Once
	ogFontSet   ogFonts
	ogFontErr   error
)

// loadOGFonts parses the card typefaces once. staticDir is consulted first when
// running with -dev so an override can be dropped in without a rebuild.
func loadOGFonts(staticDir string) (ogFonts, error) {
	ogFontsOnce.Do(func() {
		pick := func(override string, builtin []byte) (*opentype.Font, error) {
			if b := readOGFontOverride(staticDir, override); b != nil {
				if f, err := opentype.Parse(b); err == nil {
					return f, nil
				}
				// A malformed override should not take the whole card down.
				logError("og font override "+override, fmt.Errorf("unparseable, using built-in"))
			}
			return opentype.Parse(builtin)
		}

		var err error
		if ogFontSet.display, err = pick(ogFontOverrides.display, gomedium.TTF); err != nil {
			ogFontErr = fmt.Errorf("og display font: %w", err)
			return
		}
		if ogFontSet.text, err = pick(ogFontOverrides.text, goregular.TTF); err != nil {
			ogFontErr = fmt.Errorf("og text font: %w", err)
			return
		}
		if ogFontSet.mono, err = pick(ogFontOverrides.mono, gomono.TTF); err != nil {
			ogFontErr = fmt.Errorf("og mono font: %w", err)
			return
		}
	})
	return ogFontSet, ogFontErr
}

func readOGFontOverride(staticDir, rel string) []byte {
	if staticDir != "" {
		if b, err := os.ReadFile(path.Join(staticDir, strings.TrimPrefix(rel, "static/"))); err == nil {
			return b
		}
	}
	if b, err := fs.ReadFile(web.Static, rel); err == nil {
		return b
	}
	return nil
}

func ogFace(f *opentype.Font, size float64) (font.Face, error) {
	return opentype.NewFace(f, &opentype.FaceOptions{
		Size:    size,
		DPI:     72,
		Hinting: font.HintingFull,
	})
}

// ---------- card model ----------

// ogCard is everything the renderer needs; it is deliberately free of store and
// http types so the drawing code stays testable in isolation.
type ogCard struct {
	Eyebrow  string // small uppercase label, e.g. "SKILL"
	Title    string
	Subtitle string
	Footer   string // left-aligned meta line
	Badge    string // right-aligned meta, normally the host
	Site     string // wordmark
}

// ---------- drawing primitives ----------

func fillRoundRect(dst *image.RGBA, x, y, w, h, rad float32, c color.Color) {
	r := vector.NewRasterizer(dst.Bounds().Dx(), dst.Bounds().Dy())
	const k = 0.5522847 // circle-to-cubic magic constant
	if rad > w/2 {
		rad = w / 2
	}
	if rad > h/2 {
		rad = h / 2
	}
	cc := rad * k
	r.MoveTo(x+rad, y)
	r.LineTo(x+w-rad, y)
	r.CubeTo(x+w-rad+cc, y, x+w, y+rad-cc, x+w, y+rad)
	r.LineTo(x+w, y+h-rad)
	r.CubeTo(x+w, y+h-rad+cc, x+w-rad+cc, y+h, x+w-rad, y+h)
	r.LineTo(x+rad, y+h)
	r.CubeTo(x+rad-cc, y+h, x, y+h-rad+cc, x, y+h-rad)
	r.LineTo(x, y+rad)
	r.CubeTo(x, y+rad-cc, x+rad-cc, y, x+rad, y)
	r.ClosePath()
	r.Draw(dst, dst.Bounds(), image.NewUniform(c), image.Point{})
}

// strokeRoundRect fakes a stroke by filling the outer shape then punching the
// inner one back out in the fill colour, which is enough for a 1px hairline.
func strokeRoundRect(dst *image.RGBA, x, y, w, h, rad, weight float32, stroke, fill color.Color) {
	fillRoundRect(dst, x, y, w, h, rad, stroke)
	fillRoundRect(dst, x+weight, y+weight, w-2*weight, h-2*weight, rad-weight, fill)
}

func drawText(dst *image.RGBA, face font.Face, c color.Color, x, y int, s string) int {
	d := &font.Drawer{Dst: dst, Src: image.NewUniform(c), Face: face, Dot: fixed.P(x, y)}
	d.DrawString(s)
	return d.Dot.X.Round()
}

// drawTracked renders with extra letter-spacing, which is how the site styles
// its small uppercase labels.
func drawTracked(dst *image.RGBA, face font.Face, c color.Color, x, y int, s string, tracking fixed.Int26_6) {
	d := &font.Drawer{Dst: dst, Src: image.NewUniform(c), Face: face, Dot: fixed.P(x, y)}
	for _, r := range s {
		d.DrawString(string(r))
		d.Dot.X += tracking
	}
}

func textWidth(face font.Face, s string) int {
	return font.MeasureString(face, s).Round()
}

func drawTextRight(dst *image.RGBA, face font.Face, c color.Color, right, y int, s string) {
	drawText(dst, face, c, right-textWidth(face, s), y, s)
}

// wrapText greedily breaks s into at most maxLines lines of at most maxWidth
// pixels, ellipsizing the last line when it has to drop content.
func wrapText(face font.Face, s string, maxWidth, maxLines int) []string {
	words := strings.Fields(s)
	if len(words) == 0 || maxLines <= 0 {
		return nil
	}
	var lines []string
	cur := ""
	for i := 0; i < len(words); i++ {
		candidate := words[i]
		if cur != "" {
			candidate = cur + " " + words[i]
		}
		if textWidth(face, candidate) <= maxWidth {
			cur = candidate
			continue
		}
		if cur == "" {
			// A single word too long for the line: hard-break it.
			cur = truncateToWidth(face, words[i], maxWidth)
		}
		lines = append(lines, cur)
		cur = ""
		if len(lines) == maxLines {
			return ellipsizeLast(face, lines, words[i:], maxWidth)
		}
		i-- // retry this word on the next line
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	return lines
}

func ellipsizeLast(face font.Face, lines []string, remaining []string, maxWidth int) []string {
	if len(remaining) == 0 {
		return lines
	}
	last := lines[len(lines)-1]
	lines[len(lines)-1] = truncateToWidth(face, last+"…", maxWidth)
	return lines
}

func truncateToWidth(face font.Face, s string, maxWidth int) string {
	if textWidth(face, s) <= maxWidth {
		return s
	}
	r := []rune(s)
	for len(r) > 1 {
		r = r[:len(r)-1]
		candidate := strings.TrimRight(string(r), " ,.;:·-") + "…"
		if textWidth(face, candidate) <= maxWidth {
			return candidate
		}
	}
	return "…"
}

// ---------- the card ----------

// renderOGCard draws card and returns encoded PNG bytes.
func renderOGCard(staticDir string, card ogCard) ([]byte, error) {
	fonts, err := loadOGFonts(staticDir)
	if err != nil {
		return nil, err
	}

	img := image.NewRGBA(image.Rect(0, 0, ogWidth, ogHeight))
	draw.Draw(img, img.Bounds(), image.NewUniform(ogBG), image.Point{}, draw.Src)

	// Inset card, mirroring the site's --radius-card scaled up.
	const (
		cardX, cardY = 48.0, 48.0
		cardW, cardH = ogWidth - 2*cardX, ogHeight - 2*cardY
	)
	strokeRoundRect(img, cardX, cardY, cardW, cardH, 28, 1.5, ogLine, ogCardBG)

	const (
		padX       = 104 // cardX + 56
		contentTop = 104
		right      = ogWidth - padX
		maxW       = right - padX
		bottom     = ogHeight - 104
	)

	// --- wordmark: the same 2x2 mark as the favicon, accent on three cells ---
	const (
		cell = 11
		gap  = 3
	)
	markY := contentTop
	for i, c := range []color.RGBA{ogAccent, ogAccent, ogAccent, {0x2a, 0x2a, 0x28, 0xff}} {
		cx := padX + (i%2)*(cell+gap)
		cy := markY + (i/2)*(cell+gap)
		draw.Draw(img, image.Rect(cx, cy, cx+cell, cy+cell), image.NewUniform(c), image.Point{}, draw.Src)
	}

	wordFace, err := ogFace(fonts.display, 25)
	if err != nil {
		return nil, err
	}
	defer wordFace.Close()
	drawText(img, wordFace, ogFG, padX+2*cell+gap+14, markY+cell+gap+cell-2, card.Site)

	// --- accent rule: the one place colour appears as colour ---
	draw.Draw(img, image.Rect(padX, 190, padX+52, 194), image.NewUniform(ogAccent), image.Point{}, draw.Src)

	// --- eyebrow ---
	monoFace, err := ogFace(fonts.mono, 19)
	if err != nil {
		return nil, err
	}
	defer monoFace.Close()
	if card.Eyebrow != "" {
		drawTracked(img, monoFace, ogMuted, padX, 232, strings.ToUpper(card.Eyebrow), fixed.I(2))
	}

	// --- title ---
	titleSize := 68.0
	if len([]rune(card.Title)) > 26 {
		titleSize = 54
	}
	titleFace, err := ogFace(fonts.display, titleSize)
	if err != nil {
		return nil, err
	}
	defer titleFace.Close()

	lineH := int(titleSize * 1.18)
	titleLines := wrapText(titleFace, card.Title, maxW, 2)
	y := 306
	if len(titleLines) > 1 {
		y = 292
	}
	for _, ln := range titleLines {
		drawText(img, titleFace, ogFG, padX, y, ln)
		y += lineH
	}
	titleBottom := y - lineH

	// --- description ---
	bodyFace, err := ogFace(fonts.text, 29)
	if err != nil {
		return nil, err
	}
	defer bodyFace.Close()
	if card.Subtitle != "" {
		const bodyLineH = 43
		descTop := titleBottom + 62
		// Keep clear of the footer rather than letting long copy run off the card.
		maxLines := (bottom - 30 - descTop) / bodyLineH
		maxLines = clampInt(maxLines+1, 1, 3)
		dy := descTop
		for _, ln := range wrapText(bodyFace, card.Subtitle, maxW, maxLines) {
			drawText(img, bodyFace, ogMuted, padX, dy, ln)
			dy += bodyLineH
		}
	}

	// --- footer ---
	if card.Footer != "" {
		drawText(img, monoFace, ogFaint, padX, bottom, card.Footer)
	}
	if card.Badge != "" {
		drawTextRight(img, monoFace, ogFaint, right, bottom, card.Badge)
	}

	var buf bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestSpeed}
	if err := enc.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// ---------- cache ----------

type ogEntry struct {
	etag string
	png  []byte
}

var (
	ogCacheMu sync.RWMutex
	ogCache   = map[string]ogEntry{}
)

// ogCacheMax bounds memory; the whole map is dropped when it is hit, which is
// fine for a registry whose page count is measured in hundreds.
const ogCacheMax = 128

func ogCacheGet(key, etag string) []byte {
	ogCacheMu.RLock()
	defer ogCacheMu.RUnlock()
	if e, ok := ogCache[key]; ok && e.etag == etag {
		return e.png
	}
	return nil
}

func ogCachePut(key, etag string, data []byte) {
	ogCacheMu.Lock()
	defer ogCacheMu.Unlock()
	if len(ogCache) >= ogCacheMax {
		ogCache = map[string]ogEntry{}
	}
	ogCache[key] = ogEntry{etag: etag, png: data}
}

func ogETag(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return `"og-` + hex.EncodeToString(sum[:8]) + `"`
}

// ---------- handlers ----------

// ogPath is the URL of the card for a kind/name pair. Kind and name are already
// slugs, so no escaping is needed beyond what the store guarantees.
func ogPath(kind, name string) string {
	if kind == "" || name == "" {
		return "/og.png"
	}
	return "/og/" + kind + "/" + name + ".png"
}

// handleOGDefault serves the site-level card used by the home and index pages.
func (s *Server) handleOGDefault(w http.ResponseWriter, r *http.Request) {
	s.serveOG(w, r, s.defaultOGCard(r), "site")
}

func (s *Server) defaultOGCard(r *http.Request) ogCard {
	return ogCard{
		Site:     s.siteName(),
		Eyebrow:  "Registry",
		Title:    s.siteName(),
		Subtitle: s.siteDescription(),
		Footer: fmt.Sprintf("%d %s · %d %s",
			s.store.SkillCount(), plural(s.store.SkillCount(), "skill", "skills"),
			s.store.BundleCount(), plural(s.store.BundleCount(), "folder", "folders")),
		Badge: hostOf(s.baseURL(r)),
	}
}

// handleOGImage serves a card for a single skill or folder. An unknown kind or
// name falls back to the site card: a 404 here would leave a shared link with a
// broken preview, which is worse than a generic one.
func (s *Server) handleOGImage(w http.ResponseWriter, r *http.Request) {
	kind := strings.ToLower(r.PathValue("kind"))
	name := strings.TrimSuffix(r.PathValue("name"), ".png")

	switch kind {
	case "skill":
		if sk, err := s.store.Skill(name); err == nil {
			s.serveOG(w, r, s.skillOGCard(r, sk), "skill/"+name+"/"+sk.UpdatedAt.UTC().Format("20060102150405"))
			return
		}
	case "folder", "bundle":
		if b, err := s.store.Bundle(name); err == nil {
			s.serveOG(w, r, s.bundleOGCard(r, b), "folder/"+name+"/"+b.UpdatedAt.UTC().Format("20060102150405"))
			return
		}
	}
	s.serveOG(w, r, s.defaultOGCard(r), "site")
}

func (s *Server) skillOGCard(r *http.Request, sk *store.Skill) ogCard {
	footer := fmt.Sprintf("%d %s · %s", len(sk.Files)+1, plural(len(sk.Files)+1, "file", "files"), humanBytes(sk.Size))
	if len(sk.Tags) > 0 {
		footer = strings.Join(sk.Tags, " · ")
	}
	return ogCard{
		Site:     s.siteName(),
		Eyebrow:  "Skill",
		Title:    sk.Name,
		Subtitle: squashSpace(sk.Description),
		Footer:   footer,
		Badge:    hostOf(s.baseURL(r)),
	}
}

func (s *Server) bundleOGCard(r *http.Request, b *store.Bundle) ogCard {
	n := len(s.store.BundleSkills(b))
	return ogCard{
		Site:     s.siteName(),
		Eyebrow:  "Folder",
		Title:    b.DisplayTitle(),
		Subtitle: squashSpace(b.Description),
		Footer:   fmt.Sprintf("%d %s", n, plural(n, "skill", "skills")),
		Badge:    hostOf(s.baseURL(r)),
	}
}

// serveOG renders (or reuses) the card and writes it with conditional-request
// support.
func (s *Server) serveOG(w http.ResponseWriter, r *http.Request, card ogCard, key string) {
	etag := ogETag(key, card.Site, card.Eyebrow, card.Title, card.Subtitle, card.Footer, card.Badge)

	data := ogCacheGet(key, etag)
	if data == nil {
		var err error
		data, err = renderOGCard(s.staticDir(), card)
		if err != nil {
			logError("render og card", err)
			http.Error(w, "could not render image", http.StatusInternalServerError)
			return
		}
		ogCachePut(key, etag, data)
	}

	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", fmt.Sprintf("public, max-age=%d, stale-while-revalidate=604800", ogMaxAge))
	w.Header().Set("ETag", etag)
	if match := r.Header.Get("If-None-Match"); match != "" && strings.Contains(match, etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))
	if r.Method == http.MethodHead {
		return
	}
	if _, err := w.Write(data); err != nil {
		logError("write og card", err)
	}
}

func (s *Server) staticDir() string {
	if s.opts.Dev {
		return s.opts.StaticDir
	}
	return ""
}
