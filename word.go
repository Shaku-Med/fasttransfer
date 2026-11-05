package main

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
	"image"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf16"
)

// Word files. A .docx is a zip of XML parts; only the parts that map onto
// the editor's formats are read, so headings, lists, emphasis, colors, links
// and pictures come across and anything else falls back to its text.

const (
	maxDocxFile   = 100 << 20
	maxDocxPart   = 64 << 20
	maxDocxImages = 200
	maxPreviewArt = 20 << 20
)

var (
	errNotDocx   = errors.New("that is not a Word file")
	errDocxLarge = errors.New("that Word file is too large")
)

type docxRel struct {
	kind     string
	target   string
	external bool
}

type docxPara struct {
	style string
	align string
	numID string
	level int
}

type docxRun struct {
	bold, italic, underline, strike bool
	color, background, script       string
	size, font                      string
}

type docxReader struct {
	zr     *zip.Reader
	rels   map[string]docxRel
	lists  map[string]map[int]string
	styles map[string]string
	keep   func(data []byte) string
	out    delta
	units  int
	images int
	full   bool
}

// readDocx turns a Word file into editor content. keep stores a picture and
// returns the address to use for it, or "" to leave the picture out.
func readDocx(full string, keep func(data []byte) string) (delta, error) {
	st, err := os.Stat(full)
	if err != nil {
		return nil, err
	}
	if st.Size() > maxDocxFile {
		return nil, errDocxLarge
	}
	f, err := os.Open(full)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	zr, err := zip.NewReader(f, st.Size())
	if err != nil {
		return nil, errNotDocx
	}
	rd := &docxReader{zr: zr, keep: keep, rels: map[string]docxRel{}, lists: map[string]map[int]string{}, styles: map[string]string{}}
	body, err := rd.part("word/document.xml")
	if err != nil {
		return nil, errNotDocx
	}
	if raw, err := rd.part("word/_rels/document.xml.rels"); err == nil {
		rd.readRels(raw)
	}
	if raw, err := rd.part("word/numbering.xml"); err == nil {
		rd.readNumbering(raw)
	}
	if raw, err := rd.part("word/styles.xml"); err == nil {
		rd.readStyles(raw)
	}
	if err := rd.readBody(body); err != nil {
		return nil, err
	}
	if len(rd.out) == 0 || !endsWithNewline(rd.out) {
		rd.out.push(deltaOp{kind: opInsert, text: []uint16{'\n'}})
	}
	return rd.out, nil
}

func endsWithNewline(d delta) bool {
	last := d[len(d)-1]
	return last.image == "" && len(last.text) > 0 && last.text[len(last.text)-1] == '\n'
}

// part reads one file from the zip, refusing anything that claims or turns
// out to be larger than a sane document part.
func (rd *docxReader) part(name string) ([]byte, error) {
	for _, f := range rd.zr.File {
		if f.Name != name {
			continue
		}
		if f.UncompressedSize64 > maxDocxPart {
			return nil, errDocxLarge
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		defer rc.Close()
		data, err := io.ReadAll(io.LimitReader(rc, maxDocxPart+1))
		if err != nil {
			return nil, err
		}
		if len(data) > maxDocxPart {
			return nil, errDocxLarge
		}
		return data, nil
	}
	return nil, os.ErrNotExist
}

func attr(se xml.StartElement, local string) string {
	for _, a := range se.Attr {
		if a.Name.Local == local {
			return a.Value
		}
	}
	return ""
}

// on reads a Word toggle such as <w:b/> or <w:b w:val="0"/>.
func on(se xml.StartElement) bool {
	switch strings.ToLower(attr(se, "val")) {
	case "0", "false", "off", "none":
		return false
	}
	return true
}

func (rd *docxReader) readRels(raw []byte) {
	dec := xml.NewDecoder(bytes.NewReader(raw))
	for {
		tok, err := dec.Token()
		if err != nil {
			return
		}
		if se, ok := tok.(xml.StartElement); ok && se.Name.Local == "Relationship" {
			kind := attr(se, "Type")
			if i := strings.LastIndex(kind, "/"); i >= 0 {
				kind = kind[i+1:]
			}
			rd.rels[attr(se, "Id")] = docxRel{kind: kind, target: attr(se, "Target"), external: strings.EqualFold(attr(se, "TargetMode"), "External")}
		}
	}
}

func (rd *docxReader) readNumbering(raw []byte) {
	dec := xml.NewDecoder(bytes.NewReader(raw))
	abstract := map[string]map[int]string{}
	nums := map[string]string{}
	curAbs, curNum := "", ""
	level := 0
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		switch se.Name.Local {
		case "abstractNum":
			curAbs, curNum = attr(se, "abstractNumId"), ""
			abstract[curAbs] = map[int]string{}
		case "lvl":
			level, _ = strconv.Atoi(attr(se, "ilvl"))
		case "numFmt":
			if curAbs != "" && curNum == "" {
				kind := "ordered"
				if v := attr(se, "val"); v == "bullet" || v == "none" {
					kind = "bullet"
				}
				abstract[curAbs][level] = kind
			}
		case "num":
			curNum, curAbs = attr(se, "numId"), ""
		case "abstractNumId":
			if curNum != "" {
				nums[curNum] = attr(se, "val")
			}
		}
	}
	for num, abs := range nums {
		if levels, ok := abstract[abs]; ok {
			rd.lists[num] = levels
		}
	}
}

func (rd *docxReader) readStyles(raw []byte) {
	dec := xml.NewDecoder(bytes.NewReader(raw))
	cur := ""
	for {
		tok, err := dec.Token()
		if err != nil {
			return
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		switch se.Name.Local {
		case "style":
			cur = attr(se, "styleId")
		case "name":
			if cur != "" {
				rd.styles[cur] = strings.ToLower(attr(se, "val"))
				cur = ""
			}
		}
	}
}

var wordHighlights = map[string]string{
	"yellow": "#ffff00", "green": "#00ff00", "cyan": "#00ffff", "magenta": "#ff00ff",
	"blue": "#0000ff", "red": "#ff0000", "darkBlue": "#000080", "darkCyan": "#008080",
	"darkGreen": "#008000", "darkMagenta": "#800080", "darkRed": "#800000",
	"darkYellow": "#808000", "darkGray": "#808080", "lightGray": "#c0c0c0", "black": "#000000",
}

func hexColor(v string) string {
	if len(v) != 6 {
		return ""
	}
	for _, c := range strings.ToLower(v) {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return ""
		}
	}
	return "#" + strings.ToLower(v)
}

// textColor keeps a run's color unless it is black or nearly so. Word files
// often spell out black for ordinary text, which would vanish on a dark page,
// so those runs take the page's own text color instead.
func textColor(v string) string {
	c := hexColor(v)
	if c == "" {
		return ""
	}
	n, _ := strconv.ParseUint(c[1:], 16, 32)
	r, g, b := float64(n>>16&0xff), float64(n>>8&0xff), float64(n&0xff)
	if (0.299*r+0.587*g+0.114*b)/255 < 0.2 {
		return ""
	}
	return c
}

func (r docxRun) attrs(link string) map[string]any {
	a := map[string]any{}
	if r.bold {
		a["bold"] = true
	}
	if r.italic {
		a["italic"] = true
	}
	if r.underline {
		a["underline"] = true
	}
	if r.strike {
		a["strike"] = true
	}
	if r.color != "" {
		a["color"] = r.color
	}
	if r.background != "" {
		a["background"] = r.background
	}
	if r.script != "" {
		a["script"] = r.script
	}
	if r.size != "" {
		a["size"] = r.size
	}
	if r.font != "" {
		a["font"] = r.font
	}
	if link != "" {
		a["link"] = link
	}
	if len(a) == 0 {
		return nil
	}
	return a
}

func (rd *docxReader) lineAttrs(p docxPara) map[string]any {
	a := map[string]any{}
	name := rd.styles[p.style]
	if name == "" {
		name = strings.ToLower(p.style)
	}
	switch {
	case name == "title" || name == "heading 1" || name == "heading1":
		a["header"] = float64(1)
	case name == "subtitle" || name == "heading 2" || name == "heading2":
		a["header"] = float64(2)
	case strings.HasPrefix(name, "heading "):
		a["header"] = float64(3)
	case strings.Contains(name, "quote"):
		a["blockquote"] = true
	}
	kind := ""
	if p.numID != "" && p.numID != "0" {
		kind = "bullet"
		if levels, ok := rd.lists[p.numID]; ok && levels[p.level] != "" {
			kind = levels[p.level]
		}
	} else if strings.HasPrefix(name, "list bullet") {
		kind = "bullet"
	} else if strings.HasPrefix(name, "list number") {
		kind = "ordered"
	}
	if kind != "" {
		delete(a, "header")
		delete(a, "blockquote")
		a["list"] = kind
		if p.level > 0 {
			a["indent"] = float64(min(p.level, 8))
		}
	}
	if p.align != "" {
		a["align"] = p.align
	}
	if len(a) == 0 {
		return nil
	}
	return a
}

func (rd *docxReader) text(s string, attrs map[string]any) {
	if s == "" || rd.full {
		return
	}
	units := utf16.Encode([]rune(s))
	if rd.units+len(units) > maxDocUnits-1024 {
		rd.full = true
		return
	}
	rd.units += len(units)
	rd.out.push(deltaOp{kind: opInsert, text: units, attrs: attrs})
}

func (rd *docxReader) picture(relID string) {
	if rd.full || rd.images >= maxDocxImages {
		return
	}
	rel, ok := rd.rels[relID]
	if !ok || rel.external || rel.kind != "image" {
		return
	}
	name := path.Clean(path.Join("word", rel.target))
	if strings.HasPrefix(rel.target, "/") {
		name = path.Clean(strings.TrimPrefix(rel.target, "/"))
	}
	if !strings.HasPrefix(name, "word/") {
		return
	}
	data, err := rd.part(name)
	if err != nil {
		return
	}
	if src := rd.keep(data); src != "" {
		rd.images++
		rd.units++
		rd.out.push(deltaOp{kind: opInsert, image: src})
	}
}

func (rd *docxReader) readBody(raw []byte) error {
	dec := xml.NewDecoder(bytes.NewReader(raw))
	var paras []docxPara
	var run docxRun
	var links []string
	inPPr, inRun, inRPr, inText := false, false, false, false
	skip := 0
	var buf strings.Builder
	link := func() string {
		if len(links) == 0 {
			return ""
		}
		return links[len(links)-1]
	}
	endLine := func() {
		var p docxPara
		if len(paras) > 0 {
			p = paras[len(paras)-1]
		}
		if rd.full {
			return
		}
		rd.units++
		rd.out.push(deltaOp{kind: opInsert, text: []uint16{'\n'}, attrs: rd.lineAttrs(p)})
	}
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return errNotDocx
		}
		switch t := tok.(type) {
		case xml.StartElement:
			name := t.Name.Local
			// Deleted tracked changes and the old-Word copy of a drawing are
			// not part of what the reader sees.
			if skip > 0 || name == "del" || name == "Fallback" || name == "instrText" {
				skip++
				continue
			}
			switch name {
			case "p":
				paras = append(paras, docxPara{})
			case "pPr":
				inPPr = true
			case "pStyle":
				if inPPr && len(paras) > 0 {
					paras[len(paras)-1].style = attr(t, "val")
				}
			case "jc":
				if inPPr && len(paras) > 0 {
					switch attr(t, "val") {
					case "center":
						paras[len(paras)-1].align = "center"
					case "right", "end":
						paras[len(paras)-1].align = "right"
					case "both", "distribute":
						paras[len(paras)-1].align = "justify"
					}
				}
			case "ilvl":
				if inPPr && len(paras) > 0 {
					n, _ := strconv.Atoi(attr(t, "val"))
					paras[len(paras)-1].level = max(0, min(n, 8))
				}
			case "numId":
				if inPPr && len(paras) > 0 {
					paras[len(paras)-1].numID = attr(t, "val")
				}
			case "r":
				inRun = true
				run = docxRun{}
			case "rPr":
				inRPr = inRun
			case "b":
				if inRPr {
					run.bold = on(t)
				}
			case "i":
				if inRPr {
					run.italic = on(t)
				}
			case "u":
				if inRPr {
					run.underline = on(t)
				}
			case "strike", "dstrike":
				if inRPr {
					run.strike = on(t)
				}
			case "color":
				if inRPr {
					run.color = textColor(attr(t, "val"))
				}
			case "highlight":
				if inRPr {
					run.background = wordHighlights[attr(t, "val")]
				}
			case "shd":
				if inRPr && run.background == "" {
					run.background = hexColor(attr(t, "fill"))
				}
			case "vertAlign":
				if inRPr {
					switch attr(t, "val") {
					case "superscript":
						run.script = "super"
					case "subscript":
						run.script = "sub"
					}
				}
			case "sz":
				if inRPr {
					half, _ := strconv.Atoi(attr(t, "val"))
					switch {
					case half >= 44:
						run.size = "huge"
					case half >= 30:
						run.size = "large"
					case half > 0 && half <= 18:
						run.size = "small"
					}
				}
			case "rFonts":
				if inRPr {
					font := strings.ToLower(attr(t, "ascii") + " " + attr(t, "hAnsi"))
					switch {
					case strings.Contains(font, "courier") || strings.Contains(font, "consolas") || strings.Contains(font, "mono"):
						run.font = "monospace"
					case strings.Contains(font, "times") || strings.Contains(font, "georgia") || strings.Contains(font, "garamond") || strings.Contains(font, "cambria"):
						run.font = "serif"
					}
				}
			case "t":
				if inRun {
					inText = true
					buf.Reset()
				}
			case "tab":
				if inRun && !inRPr {
					rd.text("\t", run.attrs(link()))
				}
			case "br", "cr":
				if inRun && !inRPr && attr(t, "type") != "page" {
					endLine()
				}
			case "hyperlink":
				target := ""
				if rel, ok := rd.rels[attr(t, "id")]; ok && rel.external && rel.kind == "hyperlink" && safeLink(rel.target) {
					target = rel.target
				}
				links = append(links, target)
			case "blip":
				for _, a := range t.Attr {
					if a.Name.Local == "embed" {
						rd.picture(a.Value)
					}
				}
			}
		case xml.EndElement:
			if skip > 0 {
				skip--
				continue
			}
			switch t.Name.Local {
			case "t":
				if inText {
					rd.text(buf.String(), run.attrs(link()))
					inText = false
				}
			case "rPr":
				inRPr = false
			case "r":
				inRun = false
			case "pPr":
				inPPr = false
			case "hyperlink":
				if len(links) > 0 {
					links = links[:len(links)-1]
				}
			case "p":
				endLine()
				if len(paras) > 0 {
					paras = paras[:len(paras)-1]
				}
			}
		case xml.CharData:
			if inText && skip == 0 {
				buf.Write(t)
			}
		}
	}
}

// pictureOK checks a picture from inside a Word file the same way an upload
// is checked: by its bytes, and with a cap on its pixels.
func pictureOK(data []byte) (string, bool) {
	ctype := http.DetectContentType(data)
	ext := pictureExts[ctype]
	if ext == "" || len(data) > maxDocImage {
		return "", false
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width < 1 || cfg.Height < 1 || cfg.Width*cfg.Height > maxImagePixels {
		return "", false
	}
	return ext, true
}

// wordPreview answers the desk with a Word file as editor content, pictures
// inlined, for a read only page that looks like the document.
func (s *server) wordPreview(w http.ResponseWriter, r *http.Request) {
	full, _, name, err := regularInside(r.URL.Query().Get("path"))
	if err != nil || strings.ToLower(filepath.Ext(name)) != ".docx" {
		http.NotFound(w, r)
		return
	}
	budget := maxPreviewArt
	content, err := readDocx(full, func(data []byte) string {
		ext, ok := pictureOK(data)
		if !ok || len(data) > budget {
			return ""
		}
		budget -= len(data)
		return "data:" + mediaTypes[ext] + ";base64," + base64.StdEncoding.EncodeToString(data)
	})
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "this Word file could not be read"})
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"ops": content})
}

// docsImport makes a new doc from a Word file and remembers where it came
// from, so the doc can be saved back over it later.
func docsImport(w http.ResponseWriter, r *http.Request) {
	if !clipLimit.allow(clientIP(r)) {
		slowDown(w)
		return
	}
	var req struct {
		Path string `json:"path"`
	}
	if !readBody(w, r, &req) {
		return
	}
	full, _, name, err := regularInside(req.Path)
	if err != nil || strings.ToLower(filepath.Ext(name)) != ".docx" {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "that Word file is not available"})
		return
	}
	rel, _ := parseRel(req.Path)
	title := strings.TrimSuffix(name, filepath.Ext(name))
	id, err := docs.createFrom(title, "PC", rel, func(id string) (delta, error) {
		dir := docs.mediaDir(id)
		if dir == "" {
			return nil, errDocsOff
		}
		// Imported pictures count against the same caps as uploaded ones.
		budget := min(int64(maxDocMedia), int64(maxAllMedia)-mediaTotal(filepath.Dir(dir)))
		var stored int64
		return readDocx(full, func(data []byte) string {
			ext, ok := pictureOK(data)
			if !ok || stored+int64(len(data)) > budget || os.MkdirAll(dir, 0o700) != nil {
				return ""
			}
			file, err := randHex(8)
			if err != nil {
				return ""
			}
			file += "." + ext
			if writeAtom(filepath.Join(dir, file), data) != nil {
				return ""
			}
			stored += int64(len(data))
			return "/docmedia/" + id + "/" + file
		})
	})
	if err != nil {
		if errors.Is(err, errNotDocx) || errors.Is(err, errDocxLarge) || errors.Is(err, errBadChange) {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "this Word file could not be read"})
			return
		}
		docError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": id})
}

// createFrom adds a doc whose first content comes from build. Pictures that
// build stores are removed again if the doc is not kept.
func (h *docHub) createFrom(title, by, source string, build func(id string) (delta, error)) (string, error) {
	id, err := h.create(title, by)
	if err != nil {
		return "", err
	}
	content, err := build(id)
	if err == nil && (!validDoc(content) || !imagesBelong(content, id)) {
		err = errBadChange
	}
	if err != nil {
		_ = h.remove(id)
		return "", err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	d, err := h.loadLocked(id)
	if err != nil {
		return "", err
	}
	d.rec.Content = content
	d.rec.Source = source
	d.rec.Rev++
	d.dirty = true
	d.bumpLocked()
	if meta := h.index[id]; meta != nil {
		meta.Units = content.docLength()
	}
	return id, nil
}

// Writing a doc back out as a Word file.

type docxWriter struct {
	body     strings.Builder
	rels     []string
	media    map[string]string
	files    map[string][]byte
	nums     []string
	lastList string
	mediaDir string
}

func xmlEscape(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r == 0x9 || r == 0xA || r == 0xD || r >= 0x20 && r <= 0xD7FF || r >= 0xE000 && r <= 0xFFFD || r >= 0x10000 && r <= 0x10FFFF {
			_ = xml.EscapeText(&b, []byte(string(r)))
		}
	}
	return b.String()
}

func (dw *docxWriter) rel(kind, target string, external bool) string {
	id := "rId" + strconv.Itoa(len(dw.rels)+10)
	mode := ""
	if external {
		mode = ` TargetMode="External"`
	}
	dw.rels = append(dw.rels, `<Relationship Id="`+id+`" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/`+kind+`" Target="`+xmlEscape(target)+`"`+mode+`/>`)
	return id
}

func cssToHex(c string) string {
	if strings.HasPrefix(c, "#") {
		h := strings.TrimPrefix(c, "#")
		if len(h) == 3 {
			h = string([]byte{h[0], h[0], h[1], h[1], h[2], h[2]})
		}
		if len(h) == 6 {
			return strings.ToUpper(h)
		}
		return ""
	}
	var r, g, b int
	if _, err := fmt.Sscanf(strings.ReplaceAll(c, " ", ""), "rgb(%d,%d,%d)", &r, &g, &b); err != nil {
		return ""
	}
	return fmt.Sprintf("%02X%02X%02X", min(r, 255), min(g, 255), min(b, 255))
}

func (dw *docxWriter) runProps(a map[string]any) string {
	var p strings.Builder
	if attrString(a, "font") == "monospace" || attrOn(a, "code") {
		p.WriteString(`<w:rFonts w:ascii="Consolas" w:hAnsi="Consolas" w:cs="Consolas"/>`)
	} else if attrString(a, "font") == "serif" {
		p.WriteString(`<w:rFonts w:ascii="Georgia" w:hAnsi="Georgia" w:cs="Georgia"/>`)
	}
	if attrOn(a, "bold") {
		p.WriteString(`<w:b/>`)
	}
	if attrOn(a, "italic") {
		p.WriteString(`<w:i/>`)
	}
	if attrOn(a, "strike") {
		p.WriteString(`<w:strike/>`)
	}
	if c := cssToHex(attrString(a, "color")); c != "" {
		p.WriteString(`<w:color w:val="` + c + `"/>`)
	} else if attrString(a, "link") != "" {
		p.WriteString(`<w:color w:val="0563C1"/>`)
	}
	switch attrString(a, "size") {
	case "small":
		p.WriteString(`<w:sz w:val="18"/>`)
	case "large":
		p.WriteString(`<w:sz w:val="32"/>`)
	case "huge":
		p.WriteString(`<w:sz w:val="48"/>`)
	}
	if attrOn(a, "underline") || attrString(a, "link") != "" {
		p.WriteString(`<w:u w:val="single"/>`)
	}
	if c := cssToHex(attrString(a, "background")); c != "" {
		p.WriteString(`<w:shd w:val="clear" w:color="auto" w:fill="` + c + `"/>`)
	}
	switch attrString(a, "script") {
	case "super":
		p.WriteString(`<w:vertAlign w:val="superscript"/>`)
	case "sub":
		p.WriteString(`<w:vertAlign w:val="subscript"/>`)
	}
	if p.Len() == 0 {
		return ""
	}
	return "<w:rPr>" + p.String() + "</w:rPr>"
}

func (dw *docxWriter) textRun(text string, a map[string]any) string {
	props := dw.runProps(a)
	var b strings.Builder
	for i, piece := range strings.Split(text, "\t") {
		if i > 0 {
			b.WriteString("<w:r>" + props + "<w:tab/></w:r>")
		}
		if piece != "" {
			b.WriteString("<w:r>" + props + `<w:t xml:space="preserve">` + xmlEscape(piece) + "</w:t></w:r>")
		}
	}
	return b.String()
}

// imageRun places a stored picture inline, scaled down to the page width.
func (dw *docxWriter) imageRun(src string) string {
	m := mediaRe.FindStringSubmatch(src)
	if m == nil || dw.mediaDir == "" {
		return ""
	}
	id, ok := dw.media[m[2]]
	var cfg image.Config
	if !ok {
		data, err := os.ReadFile(filepath.Join(dw.mediaDir, m[2]))
		if err != nil {
			return ""
		}
		cfg, _, err = image.DecodeConfig(bytes.NewReader(data))
		if err != nil {
			return ""
		}
		dw.files["word/media/"+m[2]] = data
		id = dw.rel("image", "media/"+m[2], false)
		dw.media[m[2]] = id
	} else if data := dw.files["word/media/"+m[2]]; data != nil {
		cfg, _, _ = image.DecodeConfig(bytes.NewReader(data))
	}
	const emuPerPx, maxWidth = 9525, 5943600
	cx, cy := int64(cfg.Width)*emuPerPx, int64(cfg.Height)*emuPerPx
	if cx > maxWidth && cx > 0 {
		cy = cy * maxWidth / cx
		cx = maxWidth
	}
	n := strconv.Itoa(len(dw.media))
	return `<w:r><w:drawing><wp:inline distT="0" distB="0" distL="0" distR="0"><wp:extent cx="` + strconv.FormatInt(cx, 10) + `" cy="` + strconv.FormatInt(cy, 10) + `"/>` +
		`<wp:docPr id="` + n + `" name="Picture ` + n + `"/>` +
		`<a:graphic xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main"><a:graphicData uri="http://schemas.openxmlformats.org/drawingml/2006/picture">` +
		`<pic:pic xmlns:pic="http://schemas.openxmlformats.org/drawingml/2006/picture"><pic:nvPicPr><pic:cNvPr id="` + n + `" name="Picture ` + n + `"/><pic:cNvPicPr/></pic:nvPicPr>` +
		`<pic:blipFill><a:blip r:embed="` + id + `"/><a:stretch><a:fillRect/></a:stretch></pic:blipFill>` +
		`<pic:spPr><a:xfrm><a:off x="0" y="0"/><a:ext cx="` + strconv.FormatInt(cx, 10) + `" cy="` + strconv.FormatInt(cy, 10) + `"/></a:xfrm><a:prstGeom prst="rect"><a:avLst/></a:prstGeom></pic:spPr></pic:pic>` +
		`</a:graphicData></a:graphic></wp:inline></w:drawing></w:r>`
}

// listNum gives each run of numbered lines its own numbering, so a second
// numbered list starts again at 1. Bullets all share one.
func (dw *docxWriter) listNum(kind string) string {
	if kind != "ordered" {
		dw.lastList = kind
		return "1"
	}
	if dw.lastList != "ordered" || len(dw.nums) == 0 {
		dw.nums = append(dw.nums, strconv.Itoa(len(dw.nums)+2))
	}
	dw.lastList = kind
	return dw.nums[len(dw.nums)-1]
}

func writeDocx(rec docRecord, mediaDir string) ([]byte, error) {
	dw := &docxWriter{media: map[string]string{}, files: map[string][]byte{}, mediaDir: mediaDir}
	for _, line := range docLines(rec.Content) {
		a := line.attrs
		var pPr strings.Builder
		switch attrNumber(a, "header") {
		case 1:
			pPr.WriteString(`<w:pStyle w:val="Heading1"/>`)
		case 2:
			pPr.WriteString(`<w:pStyle w:val="Heading2"/>`)
		case 3:
			pPr.WriteString(`<w:pStyle w:val="Heading3"/>`)
		default:
			if attrOn(a, "blockquote") {
				pPr.WriteString(`<w:pStyle w:val="Quote"/>`)
			} else if a["code-block"] != nil {
				pPr.WriteString(`<w:pStyle w:val="Code"/>`)
			}
		}
		if kind := attrString(a, "list"); kind != "" {
			if kind != "ordered" {
				kind = "bullet"
			}
			pPr.WriteString(`<w:numPr><w:ilvl w:val="` + strconv.Itoa(attrNumber(a, "indent")) + `"/><w:numId w:val="` + dw.listNum(kind) + `"/></w:numPr>`)
		} else {
			dw.lastList = ""
			if n := attrNumber(a, "indent"); n > 0 {
				pPr.WriteString(`<w:ind w:left="` + strconv.Itoa(720*n) + `"/>`)
			}
		}
		switch attrString(a, "align") {
		case "center":
			pPr.WriteString(`<w:jc w:val="center"/>`)
		case "right":
			pPr.WriteString(`<w:jc w:val="right"/>`)
		case "justify":
			pPr.WriteString(`<w:jc w:val="both"/>`)
		}
		dw.body.WriteString("<w:p>")
		if pPr.Len() > 0 {
			dw.body.WriteString("<w:pPr>" + pPr.String() + "</w:pPr>")
		}
		for _, span := range line.spans {
			if span.image != "" {
				dw.body.WriteString(dw.imageRun(span.image))
				continue
			}
			runs := dw.textRun(span.text, span.attrs)
			if link := attrString(span.attrs, "link"); link != "" && safeLink(link) {
				dw.body.WriteString(`<w:hyperlink r:id="` + dw.rel("hyperlink", link, true) + `">` + runs + "</w:hyperlink>")
			} else {
				dw.body.WriteString(runs)
			}
		}
		dw.body.WriteString("</w:p>")
	}

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	put := func(name, content string) error {
		f, err := zw.Create(name)
		if err != nil {
			return err
		}
		_, err = io.WriteString(f, content)
		return err
	}
	const head = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` + "\n"
	types := head + `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">` +
		`<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>` +
		`<Default Extension="xml" ContentType="application/xml"/>` +
		`<Default Extension="png" ContentType="image/png"/><Default Extension="jpg" ContentType="image/jpeg"/>` +
		`<Default Extension="gif" ContentType="image/gif"/><Default Extension="webp" ContentType="image/webp"/>` +
		`<Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/>` +
		`<Override PartName="/word/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.styles+xml"/>` +
		`<Override PartName="/word/numbering.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.numbering+xml"/>` +
		`<Override PartName="/docProps/core.xml" ContentType="application/vnd.openxmlformats-package.core-properties+xml"/>` +
		`</Types>`
	rootRels := head + `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
		`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/>` +
		`<Relationship Id="rId2" Type="http://schemas.openxmlformats.org/package/2006/relationships/metadata/core-properties" Target="docProps/core.xml"/>` +
		`</Relationships>`
	core := head + `<cp:coreProperties xmlns:cp="http://schemas.openxmlformats.org/package/2006/metadata/core-properties" xmlns:dc="http://purl.org/dc/elements/1.1/">` +
		`<dc:title>` + xmlEscape(rec.Title) + `</dc:title></cp:coreProperties>`
	docRels := head + `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
		`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/>` +
		`<Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/numbering" Target="numbering.xml"/>` +
		strings.Join(dw.rels, "") + `</Relationships>`
	document := head + `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" xmlns:wp="http://schemas.openxmlformats.org/drawingml/2006/wordprocessingDrawing">` +
		`<w:body>` + dw.body.String() + `<w:sectPr><w:pgSz w:w="12240" w:h="15840"/><w:pgMar w:top="1440" w:right="1440" w:bottom="1440" w:left="1440" w:header="720" w:footer="720" w:gutter="0"/></w:sectPr></w:body></w:document>`
	for _, part := range []struct{ name, content string }{
		{"[Content_Types].xml", types},
		{"_rels/.rels", rootRels},
		{"docProps/core.xml", core},
		{"word/_rels/document.xml.rels", docRels},
		{"word/document.xml", document},
		{"word/styles.xml", head + docxStyles},
		{"word/numbering.xml", head + dw.numbering()},
	} {
		if err := put(part.name, part.content); err != nil {
			return nil, err
		}
	}
	for name, data := range dw.files {
		f, err := zw.Create(name)
		if err != nil {
			return nil, err
		}
		if _, err := f.Write(data); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (dw *docxWriter) numbering() string {
	var b strings.Builder
	b.WriteString(`<w:numbering xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">`)
	bullets := []string{"•", "◦", "▪"}
	b.WriteString(`<w:abstractNum w:abstractNumId="0">`)
	for l := 0; l <= 8; l++ {
		b.WriteString(fmt.Sprintf(`<w:lvl w:ilvl="%d"><w:start w:val="1"/><w:numFmt w:val="bullet"/><w:lvlText w:val="%s"/><w:lvlJc w:val="left"/><w:pPr><w:ind w:left="%d" w:hanging="360"/></w:pPr></w:lvl>`, l, bullets[l%3], 720*(l+1)))
	}
	b.WriteString(`</w:abstractNum><w:abstractNum w:abstractNumId="1">`)
	formats := []string{"decimal", "lowerLetter", "lowerRoman"}
	for l := 0; l <= 8; l++ {
		b.WriteString(fmt.Sprintf(`<w:lvl w:ilvl="%d"><w:start w:val="1"/><w:numFmt w:val="%s"/><w:lvlText w:val="%%%d."/><w:lvlJc w:val="left"/><w:pPr><w:ind w:left="%d" w:hanging="360"/></w:pPr></w:lvl>`, l, formats[l%3], l+1, 720*(l+1)))
	}
	b.WriteString(`</w:abstractNum><w:num w:numId="1"><w:abstractNumId w:val="0"/></w:num>`)
	for _, id := range dw.nums {
		b.WriteString(`<w:num w:numId="` + id + `"><w:abstractNumId w:val="1"/><w:lvlOverride w:ilvl="0"><w:startOverride w:val="1"/></w:lvlOverride></w:num>`)
	}
	b.WriteString(`</w:numbering>`)
	return b.String()
}

const docxStyles = `<w:styles xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">` +
	`<w:docDefaults><w:rPrDefault><w:rPr><w:rFonts w:ascii="Calibri" w:hAnsi="Calibri" w:cs="Calibri"/><w:sz w:val="22"/></w:rPr></w:rPrDefault>` +
	`<w:pPrDefault><w:pPr><w:spacing w:after="120" w:line="276" w:lineRule="auto"/></w:pPr></w:pPrDefault></w:docDefaults>` +
	`<w:style w:type="paragraph" w:default="1" w:styleId="Normal"><w:name w:val="Normal"/></w:style>` +
	`<w:style w:type="paragraph" w:styleId="Heading1"><w:name w:val="heading 1"/><w:basedOn w:val="Normal"/><w:next w:val="Normal"/><w:pPr><w:keepNext/><w:spacing w:before="360" w:after="120"/><w:outlineLvl w:val="0"/></w:pPr><w:rPr><w:b/><w:sz w:val="40"/></w:rPr></w:style>` +
	`<w:style w:type="paragraph" w:styleId="Heading2"><w:name w:val="heading 2"/><w:basedOn w:val="Normal"/><w:next w:val="Normal"/><w:pPr><w:keepNext/><w:spacing w:before="280" w:after="100"/><w:outlineLvl w:val="1"/></w:pPr><w:rPr><w:b/><w:sz w:val="32"/></w:rPr></w:style>` +
	`<w:style w:type="paragraph" w:styleId="Heading3"><w:name w:val="heading 3"/><w:basedOn w:val="Normal"/><w:next w:val="Normal"/><w:pPr><w:keepNext/><w:spacing w:before="240" w:after="80"/><w:outlineLvl w:val="2"/></w:pPr><w:rPr><w:b/><w:sz w:val="26"/></w:rPr></w:style>` +
	`<w:style w:type="paragraph" w:styleId="Quote"><w:name w:val="Quote"/><w:basedOn w:val="Normal"/><w:pPr><w:pBdr><w:left w:val="single" w:sz="18" w:space="12" w:color="BFBFBF"/></w:pBdr><w:ind w:left="360"/></w:pPr><w:rPr><w:i/><w:color w:val="595959"/></w:rPr></w:style>` +
	`<w:style w:type="paragraph" w:styleId="Code"><w:name w:val="Code"/><w:basedOn w:val="Normal"/><w:pPr><w:spacing w:after="0"/><w:shd w:val="clear" w:color="auto" w:fill="F2F2F2"/></w:pPr><w:rPr><w:rFonts w:ascii="Consolas" w:hAnsi="Consolas" w:cs="Consolas"/><w:sz w:val="20"/></w:rPr></w:style>` +
	`</w:styles>`
