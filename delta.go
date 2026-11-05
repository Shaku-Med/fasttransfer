package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"sort"
	"unicode/utf16"
)

// A Go port of quill-delta 5.1.0, the change format Quill speaks. The doc
// server needs compose and transform to give exactly the answers the editor
// gets in the browser, so lengths are counted in UTF-16 units like JavaScript
// and every tie is broken the same way. delta_test.go checks this against
// vectors produced by the real library.

type opKind uint8

const (
	opInsert opKind = iota + 1
	opRetain
	opDelete
)

const (
	endless = math.MaxInt
	// About a million characters. Big enough for a book, small enough that a
	// runaway client cannot grow the PC's memory without end.
	maxDocUnits = 1 << 20
)

type deltaOp struct {
	kind  opKind
	text  []uint16
	image string
	n     int
	attrs map[string]any
}

type delta []deltaOp

func (o deltaOp) length() int {
	if o.kind == opInsert {
		if o.image != "" {
			return 1
		}
		return len(o.text)
	}
	return o.n
}

func attrsEqual(a, b map[string]any) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	return reflect.DeepEqual(a, b)
}

func opsEqual(a, b deltaOp) bool {
	if a.kind != b.kind || a.n != b.n || !attrsEqual(a.attrs, b.attrs) {
		return false
	}
	if a.kind != opInsert {
		return true
	}
	if a.image != b.image || len(a.text) != len(b.text) {
		return false
	}
	for i := range a.text {
		if a.text[i] != b.text[i] {
			return false
		}
	}
	return true
}

func cloneAttrs(a map[string]any) map[string]any {
	if len(a) == 0 {
		return nil
	}
	out := make(map[string]any, len(a))
	for k, v := range a {
		out[k] = v
	}
	return out
}

func (d *delta) push(op deltaOp) {
	op.attrs = cloneAttrs(op.attrs)
	ops := *d
	index := len(ops)
	if index > 0 {
		last := ops[index-1]
		if op.kind == opDelete && last.kind == opDelete {
			ops[index-1] = deltaOp{kind: opDelete, n: last.n + op.n}
			return
		}
		// Inserting before or after a delete at the same spot is the same, so
		// inserts always go first.
		if last.kind == opDelete && op.kind == opInsert {
			index--
			if index == 0 {
				*d = append(delta{op}, ops...)
				return
			}
			last = ops[index-1]
		}
		if attrsEqual(op.attrs, last.attrs) {
			if op.kind == opInsert && last.kind == opInsert && op.image == "" && last.image == "" {
				merged := make([]uint16, 0, len(last.text)+len(op.text))
				merged = append(append(merged, last.text...), op.text...)
				ops[index-1] = deltaOp{kind: opInsert, text: merged, attrs: op.attrs}
				return
			}
			if op.kind == opRetain && last.kind == opRetain {
				ops[index-1] = deltaOp{kind: opRetain, n: last.n + op.n, attrs: op.attrs}
				return
			}
		}
	}
	if index == len(ops) {
		*d = append(ops, op)
		return
	}
	ops = append(ops, deltaOp{})
	copy(ops[index+1:], ops[index:])
	ops[index] = op
	*d = ops
}

func (d *delta) retain(n int, attrs map[string]any) {
	if n <= 0 {
		return
	}
	d.push(deltaOp{kind: opRetain, n: n, attrs: attrs})
}

func (d delta) chop() delta {
	if n := len(d); n > 0 && d[n-1].kind == opRetain && len(d[n-1].attrs) == 0 {
		return d[:n-1]
	}
	return d
}

type opIter struct {
	ops    delta
	index  int
	offset int
}

func (it *opIter) peekLength() int {
	if it.index < len(it.ops) {
		return it.ops[it.index].length() - it.offset
	}
	return endless
}

func (it *opIter) hasNext() bool { return it.peekLength() < endless }

func (it *opIter) peekKind() opKind {
	if it.index < len(it.ops) {
		return it.ops[it.index].kind
	}
	return opRetain
}

func (it *opIter) next(length int) deltaOp {
	if length <= 0 {
		length = endless
	}
	if it.index >= len(it.ops) {
		return deltaOp{kind: opRetain, n: endless}
	}
	op := it.ops[it.index]
	offset := it.offset
	opLen := op.length()
	if length >= opLen-offset {
		length = opLen - offset
		it.index++
		it.offset = 0
	} else {
		it.offset += length
	}
	switch op.kind {
	case opDelete:
		return deltaOp{kind: opDelete, n: length}
	case opRetain:
		return deltaOp{kind: opRetain, n: length, attrs: op.attrs}
	default:
		if op.image != "" {
			return deltaOp{kind: opInsert, image: op.image, attrs: op.attrs}
		}
		return deltaOp{kind: opInsert, text: op.text[offset : offset+length], attrs: op.attrs}
	}
}

func (it *opIter) rest() delta {
	if !it.hasNext() {
		return nil
	}
	if it.offset == 0 {
		return it.ops[it.index:]
	}
	offset, index := it.offset, it.index
	first := it.next(0)
	rest := it.ops[it.index:]
	it.offset, it.index = offset, index
	return append(delta{first}, rest...)
}

func composeAttrs(a, b map[string]any, keepNull bool) map[string]any {
	out := map[string]any{}
	for k, v := range b {
		if keepNull || v != nil {
			out[k] = v
		}
	}
	for k, v := range a {
		if _, inB := b[k]; !inB {
			out[k] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func transformAttrs(a, b map[string]any, priority bool) map[string]any {
	if a == nil {
		return b
	}
	if b == nil {
		return nil
	}
	if !priority {
		return b
	}
	out := map[string]any{}
	for k, v := range b {
		if _, inA := a[k]; !inA {
			out[k] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (d delta) concat(other delta) delta {
	out := append(delta(nil), d...)
	if len(other) > 0 {
		out.push(other[0])
		out = append(out, other[1:]...)
	}
	return out
}

// compose returns the change that does d and then other.
func (d delta) compose(other delta) delta {
	a := &opIter{ops: d}
	b := &opIter{ops: other}
	var out delta
	if len(other) > 0 && other[0].kind == opRetain && other[0].attrs == nil {
		firstLeft := other[0].n
		for a.peekKind() == opInsert && a.peekLength() <= firstLeft {
			firstLeft -= a.peekLength()
			out = append(out, a.next(0))
		}
		if other[0].n-firstLeft > 0 {
			b.next(other[0].n - firstLeft)
		}
	}
	for a.hasNext() || b.hasNext() {
		switch {
		case b.peekKind() == opInsert:
			out.push(b.next(0))
		case a.peekKind() == opDelete:
			out.push(a.next(0))
		default:
			length := min(a.peekLength(), b.peekLength())
			aOp := a.next(length)
			bOp := b.next(length)
			if bOp.kind == opRetain {
				next := deltaOp{kind: opRetain, n: length}
				if aOp.kind == opInsert {
					next = deltaOp{kind: opInsert, text: aOp.text, image: aOp.image}
				}
				next.attrs = composeAttrs(aOp.attrs, bOp.attrs, aOp.kind == opRetain)
				out.push(next)
				if !b.hasNext() && opsEqual(out[len(out)-1], next) {
					return out.concat(a.rest()).chop()
				}
			} else if bOp.kind == opDelete && aOp.kind == opRetain {
				out.push(bOp)
			}
		}
	}
	return out.chop()
}

// transform rewrites other so it can be applied after d. With priority, d is
// taken to have happened first when both insert at the same spot.
func (d delta) transform(other delta, priority bool) delta {
	a := &opIter{ops: d}
	b := &opIter{ops: other}
	var out delta
	for a.hasNext() || b.hasNext() {
		switch {
		case a.peekKind() == opInsert && (priority || b.peekKind() != opInsert):
			out.retain(a.next(0).length(), nil)
		case b.peekKind() == opInsert:
			out.push(b.next(0))
		default:
			length := min(a.peekLength(), b.peekLength())
			aOp := a.next(length)
			bOp := b.next(length)
			switch {
			case aOp.kind == opDelete:
			case bOp.kind == opDelete:
				out.push(bOp)
			default:
				out.retain(length, transformAttrs(aOp.attrs, bOp.attrs, priority))
			}
		}
	}
	return out.chop()
}

func (d delta) transformPosition(index int, priority bool) int {
	it := &opIter{ops: d}
	offset := 0
	for it.hasNext() && offset <= index {
		length := it.peekLength()
		kind := it.peekKind()
		it.next(0)
		if kind == opDelete {
			index -= min(length, index-offset)
			continue
		} else if kind == opInsert && (offset < index || !priority) {
			index += length
		}
		offset += length
	}
	return index
}

// baseLength is how long a document must be for this change to apply.
func (d delta) baseLength() int {
	n := 0
	for _, op := range d {
		if op.kind != opInsert {
			n += op.n
		}
	}
	return n
}

func (d delta) docLength() int {
	n := 0
	for _, op := range d {
		n += op.length()
	}
	return n
}

func (d delta) onlyInserts() bool {
	for _, op := range d {
		if op.kind != opInsert {
			return false
		}
	}
	return true
}

func (d delta) plainText() string {
	var buf []uint16
	for _, op := range d {
		if op.kind == opInsert && op.image == "" {
			buf = append(buf, op.text...)
		}
	}
	return string(utf16.Decode(buf))
}

func (o deltaOp) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	switch o.kind {
	case opInsert:
		var s []byte
		if o.image != "" {
			s, _ = json.Marshal(map[string]string{"image": o.image})
		} else {
			s, _ = json.Marshal(string(utf16.Decode(o.text)))
		}
		buf.WriteString(`"insert":`)
		buf.Write(s)
	case opRetain:
		buf.WriteString(`"retain":`)
		buf.WriteString(itoaInt(o.n))
	default:
		buf.WriteString(`"delete":`)
		buf.WriteString(itoaInt(o.n))
	}
	if len(o.attrs) > 0 {
		keys := make([]string, 0, len(o.attrs))
		for k := range o.attrs {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		buf.WriteString(`,"attributes":{`)
		for i, k := range keys {
			if i > 0 {
				buf.WriteByte(',')
			}
			kb, _ := json.Marshal(k)
			vb, err := json.Marshal(o.attrs[k])
			if err != nil {
				return nil, err
			}
			buf.Write(kb)
			buf.WriteByte(':')
			buf.Write(vb)
		}
		buf.WriteByte('}')
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

func itoaInt(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

var errBadOp = errors.New("bad delta op")

func (o *deltaOp) UnmarshalJSON(b []byte) error {
	var raw struct {
		Insert     json.RawMessage `json:"insert"`
		Retain     *json.Number    `json:"retain"`
		Delete     *json.Number    `json:"delete"`
		Attributes json.RawMessage `json:"attributes"`
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	dec.UseNumber()
	if err := dec.Decode(&raw); err != nil {
		return errBadOp
	}
	set := 0
	for _, p := range []bool{len(raw.Insert) > 0, raw.Retain != nil, raw.Delete != nil} {
		if p {
			set++
		}
	}
	if set != 1 {
		return errBadOp
	}
	*o = deltaOp{}
	switch {
	case len(raw.Insert) > 0:
		o.kind = opInsert
		var text string
		if err := json.Unmarshal(raw.Insert, &text); err == nil {
			o.text = utf16.Encode([]rune(text))
			if len(o.text) == 0 {
				return errBadOp
			}
			break
		}
		// The only embed allowed is an image, written {"image": "..."}.
		var embed map[string]string
		if err := json.Unmarshal(raw.Insert, &embed); err != nil || len(embed) != 1 || embed["image"] == "" {
			return errBadOp
		}
		o.image = embed["image"]
	case raw.Retain != nil:
		o.kind = opRetain
		n, err := raw.Retain.Int64()
		if err != nil || n <= 0 || n > maxDocUnits {
			return errBadOp
		}
		o.n = int(n)
	default:
		o.kind = opDelete
		n, err := raw.Delete.Int64()
		if err != nil || n <= 0 || n > maxDocUnits {
			return errBadOp
		}
		o.n = int(n)
	}
	if len(raw.Attributes) > 0 && string(raw.Attributes) != "null" {
		var attrs map[string]any
		if err := json.Unmarshal(raw.Attributes, &attrs); err != nil {
			return errBadOp
		}
		if o.kind == opDelete && len(attrs) > 0 {
			return errBadOp
		}
		if len(attrs) > 0 {
			o.attrs = attrs
		}
	}
	return nil
}
