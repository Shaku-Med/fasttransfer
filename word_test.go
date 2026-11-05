package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"
)

func deltaFrom(t *testing.T, raw string) delta {
	t.Helper()
	var d delta
	if err := json.Unmarshal([]byte(raw), &d); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestWordRoundTrip(t *testing.T) {
	content := deltaFrom(t, `[
		{"insert":"Quarterly plan"},{"insert":"\n","attributes":{"header":1}},
		{"insert":"Plain, "},{"insert":"bold","attributes":{"bold":true}},{"insert":", "},
		{"insert":"red","attributes":{"color":"#d93025","italic":true}},{"insert":" and "},
		{"insert":"a link","attributes":{"link":"https://example.com/a?b=1&c=2"}},{"insert":" <tag> & x²"},
		{"insert":"2","attributes":{"script":"super"}},{"insert":"\n","attributes":{"align":"center"}},
		{"insert":"First"},{"insert":"\n","attributes":{"list":"ordered"}},
		{"insert":"Second"},{"insert":"\n","attributes":{"list":"ordered"}},
		{"insert":"Nested point"},{"insert":"\n","attributes":{"list":"bullet","indent":1}},
		{"insert":"Said well"},{"insert":"\n","attributes":{"blockquote":true}},
		{"insert":"tab\there"},{"insert":"\n"}
	]`)
	body, err := writeDocx(docRecord{Title: "Plan", Content: content}, "")
	if err != nil {
		t.Fatal(err)
	}
	full := filepath.Join(t.TempDir(), "plan.docx")
	if err := os.WriteFile(full, body, 0o644); err != nil {
		t.Fatal(err)
	}
	back, err := readDocx(full, func([]byte) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if !validDoc(back) {
		t.Fatal("the read back document does not pass the editor's checks")
	}
	if got, want := back.plainText(), content.plainText(); got != want {
		t.Fatalf("text changed:\n got %q\nwant %q", got, want)
	}
	wantAttrs := map[string]map[string]any{
		"Quarterly plan": nil,
		"bold":           {"bold": true},
		"red":            {"color": "#d93025", "italic": true},
		"a link":         {"link": "https://example.com/a?b=1&c=2", "underline": true, "color": "#0563c1"},
		"2":              {"script": "super"},
	}
	for _, op := range back {
		text := string(utf16.Decode(op.text))
		if want, ok := wantAttrs[text]; ok {
			got, _ := json.Marshal(op.attrs)
			exp, _ := json.Marshal(want)
			if string(got) != string(exp) {
				t.Errorf("%q came back with %s, want %s", text, got, exp)
			}
		}
	}
	lines := docLines(back)
	checks := []struct {
		i    int
		k    string
		want any
	}{
		{0, "header", float64(1)},
		{1, "align", "center"},
		{2, "list", "ordered"},
		{3, "list", "ordered"},
		{4, "list", "bullet"},
		{4, "indent", float64(1)},
		{5, "blockquote", true},
	}
	for _, c := range checks {
		if got := lines[c.i].attrs[c.k]; got != c.want {
			t.Errorf("line %d %s = %v, want %v", c.i, c.k, got, c.want)
		}
	}
}

// A Word file can point a link anywhere. Only web, mail and phone links may
// come through, and nothing outside the document's own parts is read.
func TestWordDropsUnsafeParts(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	add := func(name, body string) {
		f, _ := zw.Create(name)
		_, _ = f.Write([]byte(body))
	}
	add("word/_rels/document.xml.rels", `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
		<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/hyperlink" Target="javascript:alert(1)" TargetMode="External"/>
		<Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/image" Target="../../secret.png"/>
		<Relationship Id="rId3" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/image" Target="https://example.com/x.png" TargetMode="External"/>
	</Relationships>`)
	add("secret.png", "not for you")
	add("word/document.xml", `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main"><w:body>
		<w:p><w:hyperlink r:id="rId1"><w:r><w:t>click</w:t></w:r></w:hyperlink></w:p>
		<w:p><w:r><w:drawing><a:blip r:embed="rId2"/></w:drawing></w:r><w:r><w:drawing><a:blip r:embed="rId3"/></w:drawing></w:r></w:p>
		<w:p><w:del><w:r><w:delText>gone</w:delText></w:r></w:del><w:r><w:t>kept</w:t></w:r></w:p>
	</w:body></w:document>`)
	_ = zw.Close()
	full := filepath.Join(t.TempDir(), "odd.docx")
	if err := os.WriteFile(full, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	asked := 0
	got, err := readDocx(full, func([]byte) string { asked++; return "" })
	if err != nil {
		t.Fatal(err)
	}
	if asked != 0 {
		t.Fatalf("pictures outside the document were read %d times", asked)
	}
	raw, _ := json.Marshal(got)
	if strings.Contains(string(raw), "javascript") || strings.Contains(string(raw), "gone") {
		t.Fatalf("unsafe content came through: %s", raw)
	}
	if got.plainText() != "click\n\nkept\n" {
		t.Fatalf("text = %q", got.plainText())
	}
}

func TestWordRejectsNonWord(t *testing.T) {
	full := filepath.Join(t.TempDir(), "fake.docx")
	_ = os.WriteFile(full, []byte("just text"), 0o644)
	if _, err := readDocx(full, func([]byte) string { return "" }); err == nil {
		t.Fatal("a file that is not a zip was accepted")
	}
}
