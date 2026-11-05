//go:build !windows

package main

import (
	"os"
	"strings"
	"time"
)

func startScheduleClock() {
	go func() {
		ticker := time.NewTicker(20 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			deliverDueOnline()
		}
	}()
}

func registerSchedule(job scheduledSend) error {
	if !validSchedule(job) {
		return errScheduled
	}
	return writeJob(job)
}

func listSchedules() ([]scheduledSend, error) {
	dir := schedulesDir()
	if dir == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, errScheduleRead
	}
	var jobs []scheduledSend
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		id := strings.TrimSuffix(name, ".json")
		job, ok := readJob(id)
		if ok {
			jobs = append(jobs, job)
		}
	}
	return jobs, nil
}

func cancelSchedule(id string) error {
	if !scheduleIDRe.MatchString(id) {
		return errScheduled
	}
	removeJob(id)
	return nil
}

func dropMissingJobs(_ []scheduledSend) {}
