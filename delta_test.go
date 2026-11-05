package main

import (
	"encoding/json"
	"os"
	"testing"
)

type deltaCase struct {
	Doc            delta `json:"doc"`
	A              delta `json:"a"`
	B              delta `json:"b"`
	DocA           delta `json:"docA"`
	AThenB         delta `json:"aThenB"`
	BOverA         delta `json:"bOverA"`
	BUnderA        delta `json:"bUnderA"`
	AOverB         delta `json:"aOverB"`
	Pos            int   `json:"pos"`
	PosA           int   `json:"posA"`
	PosAPriority   int   `json:"posAPriority"`
	Converged      delta `json:"converged"`
	ConvergedOther delta `json:"convergedOther"`
}

func deltaJSON(t *testing.T, d delta) string {
	t.Helper()
	if d == nil {
		d = delta{}
	}
	b, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestDeltaMatchesQuill replays answers made by quill-delta 5.1.0 itself
// (testdata/delta_vectors.json), so the server and the editor can never disagree.
func TestDeltaMatchesQuill(t *testing.T) {
	raw, err := os.ReadFile("testdata/delta_vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []deltaCase
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) < 100 {
		t.Fatalf("only %d vectors", len(cases))
	}
	check := func(i int, what string, got, want delta) {
		t.Helper()
		if g, w := deltaJSON(t, got), deltaJSON(t, want); g != w {
			t.Fatalf("case %d %s\n got  %s\n want %s", i, what, g, w)
		}
	}
	for i, c := range cases {
		check(i, "doc then a", c.Doc.compose(c.A), c.DocA)
		check(i, "a then b", c.A.compose(c.B), c.AThenB)
		check(i, "b over a", c.A.transform(c.B, true), c.BOverA)
		check(i, "b under a", c.A.transform(c.B, false), c.BUnderA)
		check(i, "a over b", c.B.transform(c.A, true), c.AOverB)
		check(i, "both orders meet", c.Doc.compose(c.A).compose(c.A.transform(c.B, true)), c.Converged)
		check(i, "both orders meet the other way", c.Doc.compose(c.B).compose(c.B.transform(c.A, false)), c.ConvergedOther)
		if got := c.A.transformPosition(c.Pos, false); got != c.PosA {
			t.Fatalf("case %d cursor moved to %d, want %d", i, got, c.PosA)
		}
		if got := c.A.transformPosition(c.Pos, true); got != c.PosAPriority {
			t.Fatalf("case %d cursor with priority moved to %d, want %d", i, got, c.PosAPriority)
		}
		if deltaJSON(t, c.Converged) != deltaJSON(t, c.ConvergedOther) {
			t.Fatalf("case %d the reference itself did not converge", i)
		}
	}
}

func TestDeltaJSONIsStrict(t *testing.T) {
	bad := []string{
		`[{"insert":""}]`,
		`[{"insert":"a","retain":1}]`,
		`[{"retain":0}]`,
		`[{"delete":-1}]`,
		`[{"insert":{"video":"x"}}]`,
		`[{"insert":{"image":"x","alt":"y"}}]`,
		`[{"insert":{"image":""}}]`,
		`[{"delete":2,"attributes":{"bold":true}}]`,
		`[{"insert":"a","color":"red"}]`,
		`[{"retain":1.5}]`,
	}
	for _, b := range bad {
		var d delta
		if err := json.Unmarshal([]byte(b), &d); err == nil {
			t.Fatalf("%s should be refused", b)
		}
	}
	var d delta
	if err := json.Unmarshal([]byte(`[{"insert":"😀\n","attributes":{"bold":true}}]`), &d); err != nil || d.docLength() != 3 {
		t.Fatalf("an emoji should count as two units like JavaScript, got %d %v", d.docLength(), err)
	}
}
