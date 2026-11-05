package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestManyPhones drives a crowd of phones over real loopback HTTP. Set
// FT_PHONES to change the size, and run with -short to skip it.
func TestManyPhones(t *testing.T) {
	if testing.Short() {
		t.Skip("load test")
	}
	n := 1500
	if v, err := strconv.Atoi(os.Getenv("FT_PHONES")); err == nil && v > 0 {
		n = v
	}
	s := testServer(t)
	helloLimit = newLimiter(1e6, 1e6)
	typeLimit = newLimiter(1e6, 1e6)
	payload := strings.Repeat("x", 256<<10)
	saveFile(t, "files/report.bin", payload)

	ts := httptest.NewServer(s.routes())
	defer ts.Close()
	client := &http.Client{
		Timeout:   60 * time.Second,
		Transport: &http.Transport{MaxIdleConnsPerHost: n * 2, IdleConnTimeout: 30 * time.Second},
	}
	defer client.CloseIdleConnections()

	call := func(method, path string, body io.Reader, ctype string) (*http.Response, error) {
		req, err := http.NewRequest(method, ts.URL+path, body)
		if err != nil {
			return nil, err
		}
		req.Header.Set("X-Transfer-Key", testKey)
		if ctype != "" {
			req.Header.Set("Content-Type", ctype)
		}
		return client.Do(req)
	}
	jsonCall := func(method, path, body string, out any) error {
		res, err := call(method, path, strings.NewReader(body), "application/json")
		if err != nil {
			return err
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusOK {
			b, _ := io.ReadAll(res.Body)
			return fmt.Errorf("%s %s: %d %s", method, path, res.StatusCode, b)
		}
		if out == nil {
			_, err = io.Copy(io.Discard, res.Body)
			return err
		}
		return json.NewDecoder(res.Body).Decode(out)
	}
	fanOut := func(count, width int, fn func(i int) error) error {
		var wg sync.WaitGroup
		var first atomic.Value
		next := atomic.Int64{}
		for w := 0; w < width; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for {
					i := int(next.Add(1)) - 1
					if i >= count {
						return
					}
					if err := fn(i); err != nil {
						first.CompareAndSwap(nil, err)
					}
				}
			}()
		}
		wg.Wait()
		if err, _ := first.Load().(error); err != nil {
			return err
		}
		return nil
	}
	heap := func() float64 {
		runtime.GC()
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		return float64(m.HeapAlloc) / (1 << 20)
	}

	startHeap := heap()
	sessions := make([]string, n)
	began := time.Now()
	err := fanOut(n, 64, func(i int) error {
		var out map[string]string
		body := fmt.Sprintf(`{"device":"%032x","name":"Phone %d","kind":"android"}`, i+1, i)
		if err := jsonCall("POST", "/api/phone/hello", body, &out); err != nil {
			return err
		}
		sessions[i] = out["session"]
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	connectTook := time.Since(began)

	// Phones show up over a few moments, not in the same microsecond, so only
	// a handful dial at once. The parked polls then reuse those connections.
	got := make(chan time.Duration, n)
	var sentAt atomic.Int64
	var pollErr atomic.Value
	dialing := make(chan struct{}, 64)
	for i := 0; i < n; i++ {
		go func(session string) {
			var m mailView
			dialing <- struct{}{}
			err := jsonCall("GET", "/api/phone/events?since=-1&session="+session, "", &m)
			<-dialing
			if err != nil {
				pollErr.CompareAndSwap(nil, err)
				return
			}
			for len(m.Files) == 0 {
				if err := jsonCall("GET", fmt.Sprintf("/api/phone/events?since=%d&session=%s", m.Ver, session), "", &m); err != nil {
					pollErr.CompareAndSwap(nil, err)
					return
				}
			}
			got <- time.Since(time.Unix(0, sentAt.Load()))
		}(sessions[i])
	}
	deadline := time.Now().Add(30 * time.Second)
	for hub.polls.Load() < int32(n) {
		if err, _ := pollErr.Load().(error); err != nil {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("only %d of %d phones managed to park a poll", hub.polls.Load(), n)
		}
		time.Sleep(20 * time.Millisecond)
	}
	parkedHeap := heap()
	parkedGoroutines := runtime.NumGoroutine()

	var dirOut struct {
		Devices []deviceView `json:"devices"`
	}
	dirStart := time.Now()
	req, _ := http.NewRequest("GET", ts.URL+"/api/desk/devices", nil)
	req.Host = "127.0.0.1:" + port
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = json.NewDecoder(res.Body).Decode(&dirOut)
	res.Body.Close()
	dirTook := time.Since(dirStart)
	if len(dirOut.Devices) != n {
		t.Fatalf("the directory shows %d phones, want %d", len(dirOut.Devices), n)
	}

	sentAt.Store(time.Now().UnixNano())
	req, _ = http.NewRequest("POST", ts.URL+"/api/send", strings.NewReader(`{"devices":[],"all":true,"paths":["files/report.bin"]}`))
	req.Host = "127.0.0.1:" + port
	res, err = client.Do(req)
	if err != nil || res.StatusCode != http.StatusOK {
		t.Fatalf("send to all failed: %v %v", err, res)
	}
	res.Body.Close()
	lat := make([]time.Duration, 0, n)
	timeout := time.After(30 * time.Second)
	for len(lat) < n {
		select {
		case d := <-got:
			lat = append(lat, d)
		case <-timeout:
			t.Fatalf("only %d of %d phones heard about the file", len(lat), n)
		}
	}
	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })

	began = time.Now()
	var pulled, slowest atomic.Int64
	err = fanOut(n, 256, func(i int) error {
		one := time.Now()
		defer func() {
			if d := int64(time.Since(one)); d > slowest.Load() {
				slowest.Store(d)
			}
		}()
		var m mailView
		if err := jsonCall("GET", "/api/phone/events?since=-1&session="+sessions[i], "", &m); err != nil {
			return err
		}
		res, err := call("GET", "/api/phone/file?session="+sessions[i]+"&id="+m.Files[0].ID, nil, "")
		if err != nil {
			return err
		}
		defer res.Body.Close()
		k, err := io.Copy(io.Discard, res.Body)
		if err != nil || k != int64(len(payload)) {
			return fmt.Errorf("download got %d bytes: %v", k, err)
		}
		pulled.Add(k)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	downloadTook := time.Since(began)

	senders := 100
	if senders > n {
		senders = n
	}
	shot := bytes.Repeat([]byte{0x89, 'P', 'N', 'G'}, 32<<10)
	began = time.Now()
	err = fanOut(senders, senders, func(i int) error {
		var body bytes.Buffer
		mw := multipart.NewWriter(&body)
		part, _ := mw.CreateFormFile("files", fmt.Sprintf("shot-%d.png", i))
		_, _ = part.Write(shot)
		_ = mw.Close()
		res, err := call("POST", "/api/upload?to=android&session="+sessions[i], &body, mw.FormDataContentType())
		if err != nil {
			return err
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusOK {
			b, _ := io.ReadAll(res.Body)
			return fmt.Errorf("upload %d: %d %s", i, res.StatusCode, b)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	uploadTook := time.Since(began)
	if snap := uploads.snapshot(); snap.Active != 0 {
		t.Fatalf("%d uploads are still marked active", snap.Active)
	}
	entries, _ := os.ReadDir(saveRoot() + string(os.PathSeparator) + "android")
	if len(entries) != senders {
		t.Fatalf("%d of %d uploads landed on disk", len(entries), senders)
	}

	typers := 300
	if typers > n {
		typers = n
	}
	began = time.Now()
	err = fanOut(typers*10, typers, func(i int) error {
		who := i % typers
		body := fmt.Sprintf(`{"session":"%s","text":"note %d","tab":"tab%05d"}`, sessions[who], i, who)
		return jsonCall("POST", "/api/phone/live", body, nil)
	})
	if err != nil {
		t.Fatal(err)
	}
	typeTook := time.Since(began)

	t.Logf("%d phones connected in %v", n, connectTook.Round(time.Millisecond))
	t.Logf("all %d parked: %d goroutines, heap %.1f MB (started at %.1f MB)", n, parkedGoroutines, parkedHeap, startHeap)
	t.Logf("desk directory of %d phones served in %v", n, dirTook.Round(time.Millisecond))
	t.Logf("send to all reached every phone: median %v, p99 %v, slowest %v",
		lat[len(lat)/2].Round(time.Millisecond), lat[len(lat)*99/100].Round(time.Millisecond), lat[len(lat)-1].Round(time.Millisecond))
	t.Logf("%d phones downloaded %.0f MB at once in %v, slowest single download %v", n, float64(pulled.Load())/(1<<20), downloadTook.Round(time.Millisecond), time.Duration(slowest.Load()).Round(time.Millisecond))
	t.Logf("%d phones uploaded at once in %v", senders, uploadTook.Round(time.Millisecond))
	t.Logf("%d phones typing sent %d updates in %v", typers, typers*10, typeTook.Round(time.Millisecond))
}
