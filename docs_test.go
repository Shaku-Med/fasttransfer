package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"math/rand"
	"net/http"
	"os"
	"strings"
	"testing"
	"unicode/utf16"
)

func testDocs(t *testing.T) http.Handler {
	t.Helper()
	srv := testServer(t)
	docs = &docHub{index: map[string]*docMeta{}, live: map[string]*liveDoc{}}
	if err := docs.open(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	typeLimit = newLimiter(1e6, 1e6)
	clipLimit = newLimiter(1e6, 1e6)
	return srv.routes()
}

func newDoc(t *testing.T, mux http.Handler) string {
	t.Helper()
	w := serve(mux, deskReq("POST", "/api/desk/docs/new", `{"title":"Plan"}`))
	if w.Code != http.StatusOK {
		t.Fatalf("create got %d %s", w.Code, w.Body.String())
	}
	return decode[map[string]string](t, w)["id"]
}

func mustDelta(t *testing.T, raw string) delta {
	t.Helper()
	var d delta
	if err := json.Unmarshal([]byte(raw), &d); err != nil {
		t.Fatalf("bad delta %s: %v", raw, err)
	}
	return d
}

func editReq(id, client string, seq, rev int64, d delta) string {
	b, _ := json.Marshal(map[string]any{"id": id, "client": client, "seq": seq, "rev": rev, "delta": d})
	return string(b)
}

func docText(t *testing.T, mux http.Handler, id string) (string, int64) {
	t.Helper()
	var v struct {
		Rev     int64 `json:"rev"`
		Content delta `json:"content"`
	}
	w := serve(mux, deskReq("GET", "/api/desk/docs/open?id="+id, ""))
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatalf("open failed: %d %s", w.Code, w.Body.String())
	}
	return v.Content.plainText(), v.Rev
}

// The classic case: two people type at the same place at the same moment.
// Both words survive and both editors agree on the order.
func TestSameSpotTypingKeepsBoth(t *testing.T) {
	mux := testDocs(t)
	id := newDoc(t, mux)
	serve(mux, deskReq("POST", "/api/desk/docs/edit", editReq(id, "aaaaaaaaaaaaaaaa", 1, 0, mustDelta(t, `[{"insert":"Hello world"}]`))))
	w := serve(mux, deskReq("POST", "/api/desk/docs/edit", editReq(id, "bbbbbbbbbbbbbbbb", 1, 1, mustDelta(t, `[{"retain":5},{"insert":" there"}]`))))
	if w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	w = serve(mux, deskReq("POST", "/api/desk/docs/edit", editReq(id, "cccccccccccccccc", 1, 1, mustDelta(t, `[{"retain":5},{"insert":","}]`))))
	if w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	text, rev := docText(t, mux, id)
	if text != "Hello there, world\n" || rev != 3 {
		t.Fatalf("got %q at rev %d", text, rev)
	}
}

// One person deletes a word while another is typing inside it. The typing
// survives and only the letters that were really selected go.
func TestTypingInsideADeletedWordSurvives(t *testing.T) {
	mux := testDocs(t)
	id := newDoc(t, mux)
	serve(mux, deskReq("POST", "/api/desk/docs/edit", editReq(id, "aaaaaaaaaaaaaaaa", 1, 0, mustDelta(t, `[{"insert":"one two three"}]`))))
	serve(mux, deskReq("POST", "/api/desk/docs/edit", editReq(id, "bbbbbbbbbbbbbbbb", 1, 1, mustDelta(t, `[{"retain":4},{"delete":4}]`))))
	serve(mux, deskReq("POST", "/api/desk/docs/edit", editReq(id, "cccccccccccccccc", 1, 1, mustDelta(t, `[{"retain":6},{"insert":"!!"}]`))))
	if text, _ := docText(t, mux, id); text != "one !!three\n" {
		t.Fatalf("got %q", text)
	}
}

func TestRetryIsAppliedOnce(t *testing.T) {
	mux := testDocs(t)
	id := newDoc(t, mux)
	body := editReq(id, "aaaaaaaaaaaaaaaa", 1, 0, mustDelta(t, `[{"insert":"x"}]`))
	serve(mux, deskReq("POST", "/api/desk/docs/edit", body))
	serve(mux, deskReq("POST", "/api/desk/docs/edit", body))
	if text, rev := docText(t, mux, id); text != "x\n" || rev != 1 {
		t.Fatalf("a retried change landed twice: %q rev %d", text, rev)
	}
	var v map[string]any
	_ = json.Unmarshal(serve(mux, deskReq("GET", "/api/desk/docs/open?id="+id+"&client=aaaaaaaaaaaaaaaa", "")).Body.Bytes(), &v)
	if v["seq"].(float64) != 1 {
		t.Fatalf("open should say which change of ours landed, got %v", v["seq"])
	}
}

func TestDocRejectsUnsafeOrBrokenChanges(t *testing.T) {
	mux := testDocs(t)
	id := newDoc(t, mux)
	bad := []string{
		`[{"insert":"x","attributes":{"link":"javascript:alert(1)"}}]`,
		`[{"insert":"x","attributes":{"color":"red"}}]`,
		`[{"insert":"x","attributes":{"header":7}}]`,
		`[{"retain":50},{"insert":"x"}]`,
		`[{"delete":1}]`,
	}
	for i, raw := range bad {
		w := serve(mux, deskReq("POST", "/api/desk/docs/edit", editReq(id, "aaaaaaaaaaaaaaaa", int64(i+1), 0, mustDelta(t, raw))))
		if w.Code == http.StatusOK {
			t.Fatalf("%s should be refused", raw)
		}
	}
	if text, rev := docText(t, mux, id); text != "\n" || rev != 0 {
		t.Fatalf("a refused change changed the doc: %q rev %d", text, rev)
	}
	ok := `[{"insert":"site","attributes":{"link":"https://example.com","bold":true}},{"insert":"\n","attributes":{"header":2}}]`
	if w := serve(mux, deskReq("POST", "/api/desk/docs/edit", editReq(id, "aaaaaaaaaaaaaaaa", 9, 0, mustDelta(t, ok)))); w.Code != http.StatusOK {
		t.Fatalf("a normal formatted change was refused: %s", w.Body.String())
	}
}

func TestDocsNeedAPhoneOrThisPC(t *testing.T) {
	mux := testDocs(t)
	id := newDoc(t, mux)
	r := deskReq("GET", "/api/desk/docs", "")
	r.RemoteAddr = "192.168.1.20:5000"
	if w := serve(mux, r); w.Code != http.StatusNotFound {
		t.Fatalf("desk docs from the network got %d", w.Code)
	}
	if w := serve(mux, phoneReq("GET", "/api/phone/docs?session=nope", "")); w.Code != http.StatusNotFound {
		t.Fatalf("a phone without a session got %d", w.Code)
	}
	s := hello(t, mux, "192.168.1.30", deviceKey(40), "Writer")
	w := serve(mux, phoneReq("POST", "/api/phone/docs/edit", fmt.Sprintf(`{"session":"%s","id":"%s","client":"dddddddddddddddd","seq":1,"rev":0,"delta":[{"insert":"from the phone"}]}`, s, id)))
	if w.Code != http.StatusOK {
		t.Fatalf("a phone edit got %d %s", w.Code, w.Body.String())
	}
	if w := serve(mux, phoneReq("POST", "/api/phone/docs/delete", `{"id":"`+id+`"}`)); w.Code == http.StatusOK || w.Code == http.StatusNoContent {
		t.Fatal("phones must not delete documents")
	}
	list := decode[struct {
		Docs []docListItem `json:"docs"`
	}](t, serve(mux, deskReq("GET", "/api/desk/docs", "")))
	if len(list.Docs) != 1 || list.Docs[0].By != "Writer" {
		t.Fatalf("the list should show who edited last: %+v", list.Docs)
	}
}

func TestDocsSurviveARestart(t *testing.T) {
	mux := testDocs(t)
	id := newDoc(t, mux)
	serve(mux, deskReq("POST", "/api/desk/docs/edit", editReq(id, "aaaaaaaaaaaaaaaa", 1, 0, mustDelta(t, `[{"insert":"kept"},{"insert":"\n","attributes":{"header":1}}]`))))
	serve(mux, deskReq("POST", "/api/desk/docs/rename", `{"id":"`+id+`","title":"Notes"}`))
	docs.saveDirty()
	dir := docs.dir

	docs = &docHub{index: map[string]*docMeta{}, live: map[string]*liveDoc{}}
	if err := docs.open(dir); err != nil {
		t.Fatal(err)
	}
	text, rev := docText(t, mux, id)
	if text != "kept\n\n" || rev != 1 {
		t.Fatalf("after a restart got %q rev %d", text, rev)
	}
	list := docs.list()
	if len(list) != 1 || list[0].Title != "Notes" {
		t.Fatalf("title lost: %+v", list)
	}
}

func TestExportEscapesText(t *testing.T) {
	rec := docRecord{Title: "<b>Plan</b>", Content: mustDelta(t, `[{"insert":"<script>alert(1)</script>"},{"insert":"\n"},{"insert":"bad","attributes":{"link":"https://x.test/?a=\"><img>"}},{"insert":"\n"},{"insert":"a"},{"insert":"\n","attributes":{"list":"bullet"}}]`)}
	out := docHTML(rec, "")
	if strings.Contains(out, "<script>") || strings.Contains(out, `"><img>`) || strings.Contains(out, "<b>Plan") {
		t.Fatalf("export let markup through:\n%s", out)
	}
	if !strings.Contains(out, "<ul>") || !strings.Contains(out, "&lt;script&gt;") {
		t.Fatalf("export lost structure:\n%s", out)
	}
	if md := docMarkdown(rec); !strings.Contains(md, "- a") {
		t.Fatalf("markdown lost the list:\n%s", md)
	}
}

// simEditor runs the same algorithm as web/docs.js: one change in flight,
// a buffer for the rest, and incoming changes rewritten around both.
type simEditor struct {
	client      string
	local       delta
	rev         int64
	seq         int64
	sentSeq     int64
	outstanding delta
	buffer      delta
	inFlight    bool
}

func (e *simEditor) edit(rng *rand.Rand) {
	length := e.local.docLength() - 1
	var d delta
	at := rng.Intn(length + 1)
	d.retain(at, nil)
	switch rng.Intn(4) {
	case 0, 1:
		words := []string{"a", "bc", "é", " ", "x\n", "day"}
		var attrs map[string]any
		if rng.Intn(3) == 0 {
			attrs = map[string]any{"bold": true}
		}
		d.push(deltaOp{kind: opInsert, text: utf16.Encode([]rune(words[rng.Intn(len(words))])), attrs: attrs})
	case 2:
		if left := length - at; left > 0 {
			d.push(deltaOp{kind: opDelete, n: 1 + rng.Intn(min(left, 3))})
		} else {
			return
		}
	default:
		if left := length - at; left > 0 {
			d.retain(1+rng.Intn(min(left, 4)), map[string]any{"italic": true})
		} else {
			return
		}
	}
	d = d.chop()
	if len(d) == 0 {
		return
	}
	e.local = e.local.compose(d)
	if e.outstanding == nil {
		e.outstanding = d
		e.seq++
		e.sentSeq = e.seq
	} else if e.buffer == nil {
		e.buffer = d
	} else {
		e.buffer = e.buffer.compose(d)
	}
}

func (e *simEditor) receive(ch docChange) {
	e.rev = ch.Rev
	if ch.Client == e.client && e.outstanding != nil && ch.Seq == e.sentSeq {
		e.outstanding = e.buffer
		e.buffer = nil
		e.inFlight = false
		if e.outstanding != nil {
			e.seq++
			e.sentSeq = e.seq
		}
		return
	}
	theirs := ch.Delta
	if e.outstanding != nil {
		mine := theirs.transform(e.outstanding, true)
		theirs = e.outstanding.transform(theirs, false)
		e.outstanding = mine
	}
	if e.buffer != nil {
		mine := theirs.transform(e.buffer, true)
		theirs = e.buffer.transform(theirs, false)
		e.buffer = mine
	}
	e.local = e.local.compose(theirs)
}

func TestManyEditorsConverge(t *testing.T) {
	for _, seed := range []int64{1, 7, 42, 1234, 99999} {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			mux := testDocs(t)
			id := newDoc(t, mux)
			rng := rand.New(rand.NewSource(seed))
			editors := make([]*simEditor, 5)
			for i := range editors {
				editors[i] = &simEditor{client: fmt.Sprintf("%016x", i+1), local: blankDoc()}
			}
			send := func(e *simEditor) {
				if e.outstanding == nil || e.inFlight {
					return
				}
				w := serve(mux, deskReq("POST", "/api/desk/docs/edit", editReq(id, e.client, e.sentSeq, e.rev, e.outstanding)))
				if w.Code != http.StatusOK {
					docs.mu.Lock()
					cur := docs.live[id]
					docs.mu.Unlock()
					t.Fatalf("edit refused: %d %s\nrev %d server rev %d len %d\nop %s\nlocal %s\nserver %s", w.Code, w.Body.String(), e.rev, cur.rec.Rev, cur.rec.Content.docLength(), deltaJSON(t, e.outstanding), deltaJSON(t, e.local), deltaJSON(t, cur.rec.Content))
				}
				e.inFlight = true
			}
			pull := func(e *simEditor) {
				docs.mu.Lock()
				d, err := docs.loadLocked(id)
				if err != nil {
					docs.mu.Unlock()
					t.Fatal(err)
				}
				current := d.rec.Rev
				docs.mu.Unlock()
				if current == e.rev {
					return
				}
				w := serve(mux, deskReq("GET", fmt.Sprintf("/api/desk/docs/events?id=%s&client=%s&since=%d&pver=-1", id, e.client, e.rev), ""))
				var v struct {
					Changes []docChange `json:"changes"`
				}
				if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
					t.Fatalf("events: %d %s", w.Code, w.Body.String())
				}
				for _, ch := range v.Changes {
					e.receive(ch)
				}
			}
			for step := 0; step < 1500; step++ {
				e := editors[rng.Intn(len(editors))]
				switch rng.Intn(3) {
				case 0:
					e.edit(rng)
				case 1:
					send(e)
				default:
					pull(e)
				}
			}
			for round := 0; round < 200; round++ {
				busy := false
				for _, e := range editors {
					send(e)
					pull(e)
					if e.outstanding != nil || e.buffer != nil {
						busy = true
					}
				}
				if !busy {
					break
				}
			}
			for _, e := range editors {
				pull(e)
			}
			want, _ := docText(t, mux, id)
			docs.mu.Lock()
			server := deltaJSON(t, docs.live[id].rec.Content)
			docs.mu.Unlock()
			for i, e := range editors {
				if e.outstanding != nil || e.buffer != nil {
					t.Fatalf("editor %d never finished sending", i)
				}
				if got := deltaJSON(t, e.local); got != server {
					t.Fatalf("editor %d drifted\n got  %s\n want %s", i, got, server)
				}
			}
			if len([]rune(want)) < 20 {
				t.Fatalf("the test barely typed anything: %q", want)
			}
		})
	}
}

func TestNewFormatsAreChecked(t *testing.T) {
	mux := testDocs(t)
	id := newDoc(t, mux)
	good := `[{"insert":"a","attributes":{"font":"serif","size":"large","color":"#c0392b","background":"rgb(1, 2, 3)","script":"super"}}]`
	if w := serve(mux, deskReq("POST", "/api/desk/docs/edit", editReq(id, "aaaaaaaaaaaaaaaa", 1, 0, mustDelta(t, good)))); w.Code != http.StatusOK {
		t.Fatalf("toolbar formats were refused: %s", w.Body.String())
	}
	for i, bad := range []string{
		`[{"insert":"b","attributes":{"color":"red; background:url(x)"}}]`,
		`[{"insert":"b","attributes":{"font":"comic"}}]`,
		`[{"insert":"b","attributes":{"size":"12px"}}]`,
		`[{"insert":"b","attributes":{"script":"middle"}}]`,
	} {
		if w := serve(mux, deskReq("POST", "/api/desk/docs/edit", editReq(id, "aaaaaaaaaaaaaaaa", int64(i+2), 1, mustDelta(t, bad)))); w.Code == http.StatusOK {
			t.Fatalf("%s should be refused", bad)
		}
	}
}

func tinyPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 4, 3))
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestDocPictures(t *testing.T) {
	srv := testServer(t)
	docs = &docHub{index: map[string]*docMeta{}, live: map[string]*liveDoc{}}
	if err := docs.open(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	clipLimit = newLimiter(1e6, 1e6)
	typeLimit = newLimiter(1e6, 1e6)
	mux := srv.routes()
	id := newDoc(t, mux)
	other := newDoc(t, mux)

	r := deskReq("POST", "/api/desk/docs/image?id="+id, string(tinyPNG(t)))
	r.Header.Set("Content-Type", "image/png")
	w := serve(mux, r)
	if w.Code != http.StatusOK {
		t.Fatalf("upload got %d %s", w.Code, w.Body.String())
	}
	url := decode[map[string]string](t, w)["url"]
	if !strings.HasPrefix(url, "/docmedia/"+id+"/") {
		t.Fatalf("unexpected url %q", url)
	}
	if w := serve(mux, deskReq("POST", "/api/desk/docs/image?id="+id, "<svg onload=alert(1)></svg>")); w.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("a non picture got %d", w.Code)
	}

	embed := `[{"insert":{"image":"` + url + `"}}]`
	if w := serve(mux, deskReq("POST", "/api/desk/docs/edit", editReq(id, "aaaaaaaaaaaaaaaa", 1, 0, mustDelta(t, embed)))); w.Code != http.StatusOK {
		t.Fatalf("inserting the picture got %s", w.Body.String())
	}
	if w := serve(mux, deskReq("POST", "/api/desk/docs/edit", editReq(other, "aaaaaaaaaaaaaaaa", 1, 0, mustDelta(t, embed)))); w.Code == http.StatusOK {
		t.Fatal("a picture from one doc was let into another")
	}
	for _, bad := range []string{`https://evil.test/x.png`, `/docmedia/` + id + `/../../secret.png`, `data:image/png;base64,AAAA`} {
		raw := `[{"insert":{"image":"` + bad + `"}}]`
		if w := serve(mux, deskReq("POST", "/api/desk/docs/edit", editReq(id, "bbbbbbbbbbbbbbbb", 1, 1, mustDelta(t, raw)))); w.Code == http.StatusOK {
			t.Fatalf("%s should be refused", bad)
		}
	}

	if w := serve(mux, deskReq("GET", url, "")); w.Code != http.StatusOK || w.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("this PC could not load the picture: %d", w.Code)
	}
	r = phoneReq("GET", url, "")
	if w := serve(mux, r); w.Code != http.StatusOK {
		t.Fatalf("a device with the key could not load the picture: %d", w.Code)
	}
	r = phoneReq("GET", url, "")
	r.Header.Del("X-Transfer-Key")
	if w := serve(mux, r); w.Code != http.StatusNotFound {
		t.Fatalf("a stranger loaded the picture: %d", w.Code)
	}

	rec, _ := docs.snapshot(id)
	if out := docHTML(rec, docs.mediaDir(id)); !strings.Contains(out, "data:image/png;base64,") {
		t.Fatal("the exported page should carry the picture with it")
	}
	dir := docs.mediaDir(id)
	serve(mux, deskReq("POST", "/api/desk/docs/delete", `{"id":"`+id+`"}`))
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("deleting a doc should delete its pictures")
	}
}
