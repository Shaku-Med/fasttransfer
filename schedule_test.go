//go:build windows

package main

import (
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func useBuiltExe(t *testing.T) {
	t.Helper()
	for _, name := range []string{"FastTransfer.exe", "FastTransfer.build.exe"} {
		exe, err := filepath.Abs(name)
		if err != nil {
			t.Fatal(err)
		}
		st, err := os.Stat(exe)
		if err != nil || st.IsDir() {
			continue
		}
		scheduleExe = exe
		t.Cleanup(func() { scheduleExe = "" })
		return
	}
	t.Fatal("FastTransfer.exe is required to prove the scheduled task")
}

func queryFastTransferTasks(t *testing.T) string {
	t.Helper()
	cmd := exec.Command("schtasks", "/Query", "/TN", scheduleFolder+`\`, "/FO", "LIST", "/V")
	out, err := cmd.CombinedOutput()
	text := string(out)
	low := strings.ToLower(text)
	if err != nil && !strings.Contains(low, "cannot find") && !strings.Contains(low, "does not exist") {
		t.Fatalf("schtasks query: %v\n%s", err, text)
	}
	return text
}

func taskBlock(text, id string) string {
	for _, block := range strings.Split(text, "\r\n\r\n") {
		if strings.Contains(strings.ToLower(block), id) {
			return block
		}
	}
	return ""
}

func taskRunLine(block string) string {
	for _, line := range strings.Split(block, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Task To Run:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "Task To Run:"))
		}
	}
	return ""
}

func assertCleanCommand(t *testing.T, text, id string) {
	t.Helper()
	block := taskBlock(text, id)
	if block == "" || !strings.Contains(strings.ToLower(block), `\fasttransfer\send-`+id) {
		t.Fatalf("task is not \\FastTransfer\\send-%s:\n%s", id, text)
	}
	if strings.Contains(strings.ToUpper(text), "UTF-16") {
		t.Fatalf("task text is still corrupted:\n%s", text)
	}
	run := taskRunLine(block)
	fields := strings.Fields(run)
	if len(fields) != 2 {
		t.Fatalf("command should be the exe and a guid: %q", run)
	}
	if !strings.EqualFold(strings.Trim(fields[1], `"`), id) {
		t.Fatalf("argument is not the guid: %q", run)
	}
	exe := strings.ToLower(strings.Trim(fields[0], `"`))
	if !strings.HasSuffix(exe, "fasttransfer.exe") && !strings.HasSuffix(exe, "fasttransfer.build.exe") {
		t.Fatalf("command is not FastTransfer: %q", run)
	}
	for _, banned := range []string{"a.txt", "--deliver", "cmd.exe", "powershell", "away phone", "later phone", "future phone"} {
		if strings.Contains(strings.ToLower(block), banned) {
			t.Fatalf("task contains %s:\n%s", banned, block)
		}
	}
}

func jobsFor(t *testing.T, key string) []scheduledSend {
	t.Helper()
	jobs, err := listSchedules()
	if err != nil {
		t.Fatalf("list %v schtasks: %s", err, lastScheduleErr)
	}
	var mine []scheduledSend
	for _, job := range jobs {
		if sameDevice(job.Device, key) {
			mine = append(mine, job)
		}
	}
	return mine
}

func scheduleAway(t *testing.T, mux http.Handler, ip, key, label, when string) string {
	t.Helper()
	session := hello(t, mux, ip, key, label)
	markAway(t, mux, session)
	handle := handleOf(t, mux, label)
	body := `{"devices":["` + handle + `"],"paths":["files/a.txt"]`
	if when != "" {
		body += `,"when":"` + when + `"`
	}
	body += `}`
	w := serve(mux, deskReq("POST", "/api/send", body))
	if w.Code != 200 {
		t.Fatalf("send %d %s schtasks: %s", w.Code, w.Body.String(), lastScheduleErr)
	}
	res := decode[map[string]int](t, w)
	if res["scheduled"] != 1 || res["devices"] != 0 || res["count"] != 1 {
		t.Fatalf("send result %v", res)
	}
	jobs := jobsFor(t, key)
	if len(jobs) != 1 {
		t.Fatalf("list %d jobs root %s debug %s", len(jobs), scheduleRoot, lastScheduleErr)
	}
	id := jobs[0].ID
	t.Cleanup(func() { _ = cancelSchedule(id) })
	return id
}

func offerCount(key string) int {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	d := hub.byKey[key]
	if d == nil {
		return 0
	}
	return len(d.offers)
}

func TestFolderDropsCorruptTasks(t *testing.T) {
	if _, err := listSchedules(); err != nil {
		t.Fatalf("list %v %s", err, lastScheduleErr)
	}
	text := queryFastTransferTasks(t)
	if strings.Contains(strings.ToUpper(text), "UTF-16") {
		t.Fatalf("corrupt task remains:\n%s", text)
	}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "TaskName:") {
			continue
		}
		name := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(line, "TaskName:")))
		if !strings.Contains(name, `\fasttransfer\send-`) {
			t.Fatalf("task name is not a send id: %s", name)
		}
	}
}

func TestCancelRemovesScheduledTask(t *testing.T) {
	mux := testServer(t).routes()
	saveFile(t, "files/a.txt", "note")
	useBuiltExe(t)
	key := deviceKey(81)
	id := scheduleAway(t, mux, "192.168.1.81", key, "Away Phone", "")
	if offerCount(key) != 0 {
		t.Fatalf("an away phone was offered the file early")
	}
	listed := queryFastTransferTasks(t)
	assertCleanCommand(t, listed, id)
	gone := serve(mux, deskReq("POST", "/api/schedule/cancel", `{"id":"`+id+`"}`))
	if gone.Code != 200 {
		t.Fatalf("cancel %d %s", gone.Code, gone.Body.String())
	}
	after := queryFastTransferTasks(t)
	if strings.Contains(strings.ToLower(after), id) {
		t.Fatalf("canceled task is still in Task Scheduler:\n%s", after)
	}
	body := serve(mux, deskReq("GET", "/api/schedule", "")).Body.String()
	if strings.Contains(body, id) {
		t.Fatalf("canceled task is still in the app list: %s", body)
	}
}

func TestDueItemOfferedOnceOnConnect(t *testing.T) {
	mux := testServer(t).routes()
	saveFile(t, "files/a.txt", "note")
	useBuiltExe(t)
	key := deviceKey(82)
	id := scheduleAway(t, mux, "192.168.1.82", key, "Later Phone", "")
	listed := queryFastTransferTasks(t)
	assertCleanCommand(t, listed, id)

	again := hello(t, mux, "192.168.1.82", key, "Later Phone")
	got := mail(t, mux, again)
	if len(got.Files) != 1 {
		t.Fatalf("connect did not offer the file: %+v", got)
	}
	after := queryFastTransferTasks(t)
	if strings.Contains(strings.ToLower(after), id) {
		t.Fatalf("task remained after delivery:\n%s", after)
	}
	third := hello(t, mux, "192.168.1.82", key, "Later Phone")
	if n := len(mail(t, mux, third).Files); n != 1 {
		t.Fatalf("the file was offered %d times", n)
	}
}

func TestMissingPathIsNotSent(t *testing.T) {
	mux := testServer(t).routes()
	saveFile(t, "files/a.txt", "note")
	useBuiltExe(t)
	key := deviceKey(83)
	id := scheduleAway(t, mux, "192.168.1.83", key, "Missing Phone", "")
	jobs := jobsFor(t, key)
	if len(jobs) != 1 || jobs[0].Path == "" {
		t.Fatal("scheduled path was not stored")
	}
	if err := os.Remove(jobs[0].Path); err != nil {
		t.Fatal(err)
	}
	session := hello(t, mux, "192.168.1.83", key, "Missing Phone")
	if n := len(mail(t, mux, session).Files); n != 0 {
		t.Fatalf("missing file was offered: %d", n)
	}
	body := serve(mux, deskReq("GET", "/api/schedule", "")).Body.String()
	if !strings.Contains(body, id) || !strings.Contains(body, `"missing":true`) {
		t.Fatalf("missing item left the list: %s", body)
	}
	listed := queryFastTransferTasks(t)
	if !strings.Contains(strings.ToLower(listed), id) {
		t.Fatalf("missing item was deleted from Task Scheduler:\n%s", listed)
	}
}

func TestFutureTimeDoesNotSendEarly(t *testing.T) {
	mux := testServer(t).routes()
	saveFile(t, "files/a.txt", "note")
	useBuiltExe(t)
	key := deviceKey(84)
	when := time.Now().Add(2 * time.Hour).Format(time.RFC3339)
	id := scheduleAway(t, mux, "192.168.1.84", key, "Future Phone", when)
	session := hello(t, mux, "192.168.1.84", key, "Future Phone")
	if n := len(mail(t, mux, session).Files); n != 0 {
		t.Fatalf("future send was offered on connect: %d", n)
	}
	run := serve(mux, deskReq("POST", "/api/schedule/run", `{"id":"`+id+`"}`))
	if run.Code != 200 {
		t.Fatalf("run %d %s", run.Code, run.Body.String())
	}
	if n := len(mail(t, mux, session).Files); n != 0 {
		t.Fatalf("future send was offered early: %d", n)
	}
	listed := queryFastTransferTasks(t)
	if !strings.Contains(strings.ToLower(listed), id) {
		t.Fatalf("future task was removed:\n%s", listed)
	}
	assertCleanCommand(t, listed, id)
}
