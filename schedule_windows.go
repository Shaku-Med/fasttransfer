//go:build windows

package main

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	ole "github.com/go-ole/go-ole"
	"github.com/go-ole/go-ole/oleutil"
)

func init() {
	errScheduled = errors.New("Windows could not add that send to Task Scheduler. It was not queued.")
}

func taskCommand() string {
	if scheduleExe != "" {
		return scheduleExe
	}
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return exe
}

func startScheduleClock() {}

var lastScheduleErr string

func registerSchedule(job scheduledSend) error {
	if !validSchedule(job) {
		return errScheduled
	}
	exe := taskCommand()
	if exe == "" || !actionOK(exe, job.ID, job.ID) {
		return errScheduled
	}
	if err := writeJob(job); err != nil {
		return err
	}
	err := withService(func(svc, folder *ole.IDispatch) error {
		return defineTask(svc, folder, job, exe)
	})
	if err != nil {
		lastScheduleErr = err.Error()
		removeJob(job.ID)
		return errScheduled
	}
	lastScheduleErr = ""
	return nil
}

func listSchedules() ([]scheduledSend, error) {
	var jobs []scheduledSend
	err := withService(func(svc, folder *ole.IDispatch) error {
		rows, err := readTasks(folder)
		if err != nil {
			return err
		}
		for _, row := range rows {
			id := scheduleID(row.name)
			if !scheduleIDRe.MatchString(id) || !actionOK(row.path, row.args, id) {
				_ = deleteNamed(folder, row.name)
				continue
			}
			job, ok := readJob(id)
			if !ok {
				job = scheduledSend{ID: id, Label: "Phone"}
			}
			job.Task = scheduleFolder + `\` + taskName(id)
			jobs = append(jobs, job)
		}
		return nil
	})
	if err != nil {
		lastScheduleErr = err.Error()
		return nil, errScheduleRead
	}
	return jobs, nil
}

func cancelSchedule(id string) error {
	if !scheduleIDRe.MatchString(id) {
		return errScheduled
	}
	err := withService(func(svc, folder *ole.IDispatch) error {
		return deleteNamed(folder, taskName(id))
	})
	if err != nil && !missingTask(err) {
		lastScheduleErr = err.Error()
		return errScheduled
	}
	removeJob(id)
	return nil
}

func dropMissingJobs(live []scheduledSend) {
	dir := schedulesDir()
	if dir == "" {
		return
	}
	keep := map[string]bool{}
	for _, job := range live {
		keep[job.ID] = true
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		id := strings.TrimSuffix(name, ".json")
		if scheduleIDRe.MatchString(id) && !keep[id] {
			removeJob(id)
		}
	}
}

func scheduleID(name string) string {
	low := strings.ToLower(strings.TrimSpace(name))
	if !strings.HasPrefix(low, "send-") {
		return ""
	}
	return strings.TrimPrefix(low, "send-")
}

func actionOK(path, args, id string) bool {
	base := strings.ToLower(filepath.Base(strings.Trim(path, `"`)))
	if base != "fasttransfer.exe" && base != "fasttransfer.build.exe" {
		return false
	}
	low := strings.ToLower(path)
	if strings.Contains(low, "cmd.exe") || strings.Contains(low, "powershell") {
		return false
	}
	got := strings.ToLower(strings.Trim(strings.TrimSpace(args), `"`))
	return scheduleIDRe.MatchString(got) && got == id
}

type taskRow struct {
	name, path, args string
}

func withService(fn func(svc, folder *ole.IDispatch) error) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := ole.CoInitializeEx(0, ole.COINIT_APARTMENTTHREADED); err != nil {
		return err
	}
	defer ole.CoUninitialize()
	unknown, err := oleutil.CreateObject("Schedule.Service")
	if err != nil {
		return err
	}
	defer unknown.Release()
	svc, err := unknown.QueryInterface(ole.IID_IDispatch)
	if err != nil {
		return err
	}
	defer svc.Release()
	if v, err := oleutil.CallMethod(svc, "Connect"); err != nil {
		if v != nil {
			v.Clear()
		}
		return err
	} else if v != nil {
		v.Clear()
	}
	folderVar, folder, err := openFolder(svc)
	if err != nil {
		return err
	}
	defer folderVar.Clear()
	return fn(svc, folder)
}

func openFolder(svc *ole.IDispatch) (*ole.VARIANT, *ole.IDispatch, error) {
	rootVar, err := oleutil.CallMethod(svc, "GetFolder", `\`)
	if err != nil {
		return nil, nil, err
	}
	defer rootVar.Clear()
	root := rootVar.ToIDispatch()
	if root == nil {
		return nil, nil, errScheduleRead
	}
	folderVar, err := oleutil.CallMethod(root, "GetFolder", scheduleFolder)
	if err == nil && folderVar != nil && folderVar.ToIDispatch() != nil {
		return folderVar, folderVar.ToIDispatch(), nil
	}
	if folderVar != nil {
		folderVar.Clear()
	}
	made, err := oleutil.CallMethod(root, "CreateFolder", "FastTransfer", "")
	if err != nil || made == nil || made.ToIDispatch() == nil {
		if made != nil {
			made.Clear()
		}
		if err == nil {
			err = errScheduleRead
		}
		return nil, nil, err
	}
	return made, made.ToIDispatch(), nil
}

func defineTask(svc, folder *ole.IDispatch, job scheduledSend, exe string) error {
	defVar, err := oleutil.CallMethod(svc, "NewTask", 0)
	if err != nil || defVar == nil {
		return errOr(err)
	}
	defer defVar.Clear()
	def := defVar.ToIDispatch()
	if def == nil {
		return errScheduled
	}
	if err := putProp(dispProp(def, "RegistrationInfo"), "Description", "FastTransfer scheduled send"); err != nil {
		return err
	}
	if err := putProp(dispProp(def, "Principal"), "LogonType", 3); err != nil {
		return err
	}
	if err := putProp(dispProp(def, "Principal"), "RunLevel", 0); err != nil {
		return err
	}
	settings := dispProp(def, "Settings")
	if settings == nil {
		return errScheduled
	}
	for _, pair := range []struct {
		name string
		val  interface{}
	}{
		{"Enabled", true},
		{"Hidden", false},
		{"AllowDemandStart", true},
		{"StartWhenAvailable", !job.When.IsZero()},
		{"DisallowStartIfOnBatteries", false},
		{"StopIfGoingOnBatteries", false},
		{"ExecutionTimeLimit", "PT5M"},
		{"MultipleInstances", 2},
	} {
		if err := putProp(settings, pair.name, pair.val); err != nil {
			return err
		}
	}
	if !job.When.IsZero() {
		triggers := dispProp(def, "Triggers")
		if triggers == nil {
			return errScheduled
		}
		trVar, err := oleutil.CallMethod(triggers, "Create", 1)
		if err != nil || trVar == nil || trVar.ToIDispatch() == nil {
			if trVar != nil {
				trVar.Clear()
			}
			return errOr(err)
		}
		trigger := trVar.ToIDispatch()
		boundary := job.When.In(time.Local).Format("2006-01-02T15:04:05")
		err = putProp(trigger, "StartBoundary", boundary)
		if err == nil {
			err = putProp(trigger, "Enabled", true)
		}
		trVar.Clear()
		if err != nil {
			return err
		}
	}
	actions := dispProp(def, "Actions")
	if actions == nil {
		return errScheduled
	}
	actVar, err := oleutil.CallMethod(actions, "Create", 0)
	if err != nil || actVar == nil || actVar.ToIDispatch() == nil {
		if actVar != nil {
			actVar.Clear()
		}
		return errOr(err)
	}
	action := actVar.ToIDispatch()
	if err := putProp(action, "Path", exe); err != nil {
		actVar.Clear()
		return err
	}
	if err := putProp(action, "Arguments", job.ID); err != nil {
		actVar.Clear()
		return err
	}
	actVar.Clear()
	reg, err := oleutil.CallMethod(folder, "RegisterTaskDefinition", taskName(job.ID), def, 6, "", "", 3, "")
	if reg != nil {
		reg.Clear()
	}
	return err
}

func readTasks(folder *ole.IDispatch) ([]taskRow, error) {
	tasksVar, err := oleutil.CallMethod(folder, "GetTasks", 0)
	if err != nil || tasksVar == nil {
		return nil, errOr(err)
	}
	defer tasksVar.Clear()
	tasks := tasksVar.ToIDispatch()
	if tasks == nil {
		return nil, errScheduleRead
	}
	countVar, err := oleutil.GetProperty(tasks, "Count")
	if err != nil || countVar == nil {
		return nil, errOr(err)
	}
	n := int(countVar.Val)
	countVar.Clear()
	rows := make([]taskRow, 0, n)
	for i := 1; i <= n; i++ {
		itemVar, err := oleutil.GetProperty(tasks, "Item", i)
		if err != nil || itemVar == nil || itemVar.ToIDispatch() == nil {
			if itemVar != nil {
				itemVar.Clear()
			}
			continue
		}
		task := itemVar.ToIDispatch()
		path, args := execAction(task)
		rows = append(rows, taskRow{name: propString(task, "Name"), path: path, args: args})
		itemVar.Clear()
	}
	return rows, nil
}

func execAction(task *ole.IDispatch) (string, string) {
	def := dispProp(task, "Definition")
	if def == nil {
		return "", ""
	}
	actions := dispProp(def, "Actions")
	if actions == nil {
		return "", ""
	}
	countVar, err := oleutil.GetProperty(actions, "Count")
	if err != nil || countVar == nil {
		return "", ""
	}
	n := int(countVar.Val)
	countVar.Clear()
	if n != 1 {
		return "", ""
	}
	actVar, err := oleutil.GetProperty(actions, "Item", 1)
	if err != nil || actVar == nil || actVar.ToIDispatch() == nil {
		if actVar != nil {
			actVar.Clear()
		}
		return "", ""
	}
	defer actVar.Clear()
	act := actVar.ToIDispatch()
	return propString(act, "Path"), propString(act, "Arguments")
}

func deleteNamed(folder *ole.IDispatch, name string) error {
	if strings.TrimSpace(name) == "" {
		return errScheduled
	}
	v, err := oleutil.CallMethod(folder, "DeleteTask", name, 0)
	if v != nil {
		v.Clear()
	}
	return err
}

func dispProp(d *ole.IDispatch, name string) *ole.IDispatch {
	if d == nil {
		return nil
	}
	v, err := oleutil.GetProperty(d, name)
	if err != nil || v == nil {
		return nil
	}
	return v.ToIDispatch()
}

func propString(d *ole.IDispatch, name string) string {
	if d == nil {
		return ""
	}
	v, err := oleutil.GetProperty(d, name)
	if err != nil || v == nil {
		return ""
	}
	defer v.Clear()
	return strings.TrimSpace(v.ToString())
}

func putProp(d *ole.IDispatch, name string, val interface{}) error {
	if d == nil {
		return errScheduled
	}
	v, err := oleutil.PutProperty(d, name, val)
	if v != nil {
		v.Clear()
	}
	if err != nil {
		return err
	}
	return nil
}

func errOr(err error) error {
	if err == nil {
		return errScheduled
	}
	return err
}

func missingTask(err error) bool {
	if err == nil {
		return false
	}
	var oe *ole.OleError
	if errors.As(err, &oe) {
		code := uint32(oe.Code())
		if code == 0x80070002 || code == 0x80070003 || code == 0x8004130F {
			return true
		}
	}
	low := strings.ToLower(err.Error())
	return strings.Contains(low, "cannot find") || strings.Contains(low, "does not exist") || strings.Contains(low, "not found")
}
