package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInboxSocialFromPhone(t *testing.T) {
	testServer(t)
	const handle = "0123456789abcdef"
	hub.mu.Lock()
	hub.byHandle[handle] = &device{handle: handle, name: "Pixel 8 · Android 15"}
	hub.mu.Unlock()

	item := receiveSocial("Social media/instagram.com/Video_by_whoisfracefrose-DfNWH4AsmHT.mp4", handle)
	if item.Kind != "social" || item.Source != "Pixel 8 · Android 15" {
		t.Fatalf("source %+v", item)
	}
	if item.Folder != "Social media/instagram.com" || item.Site != "Instagram" {
		t.Fatalf("place %+v", item)
	}
	if item.Name != "Video_by_whoisfracefrose-DfNWH4AsmHT.mp4" {
		t.Fatal(item.Name)
	}
	if item.Toast != "Saved from Instagram · Video_by_whoisfracefrose-DfNWH4AsmHT.mp4" {
		t.Fatal(item.Toast)
	}
	if item.Rel != "Social media/instagram.com/Video_by_whoisfracefrose-DfNWH4AsmHT.mp4" || item.Seen {
		t.Fatalf("rel %+v", item)
	}
	items, unseen := inboxSnapshot()
	if len(items) != 1 || unseen != 1 || items[0].ID != item.ID {
		t.Fatalf("list %d unseen %d", len(items), unseen)
	}

	path := inboxFile
	useInboxFile(path)
	items, unseen = inboxSnapshot()
	if len(items) != 1 || unseen != 1 || items[0].Toast != item.Toast {
		t.Fatalf("reload %+v unseen %d", items, unseen)
	}
}

func TestInboxUploadScheduleAndLimits(t *testing.T) {
	testServer(t)
	up := receiveUpload("Kitchen · iOS 18.6", "files", "notes.txt")
	if up.Toast != "Saved from Kitchen · iOS 18.6 · notes.txt" || up.Folder != "Files" || up.Rel != "files/notes.txt" {
		t.Fatalf("upload %+v", up)
	}
	if receiveUpload("", "nope", "x").ID != "" {
		t.Fatal("bad destination was stored")
	}
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	off := receiveOffered("Office PC · Windows 11", outside, "outside.txt")
	if off.Kind != "schedule" || off.Toast != "Offered to Office PC · Windows 11 · outside.txt" || off.Rel != "" {
		t.Fatalf("offer %+v", off)
	}

	for i := 0; i < inboxMax+5; i++ {
		receiveUpload("A device", "files", "f.txt")
	}
	items, unseen := inboxSnapshot()
	if len(items) != inboxMax {
		t.Fatalf("cap %d", len(items))
	}
	if items[0].Time < items[len(items)-1].Time {
		t.Fatal("newest should be first")
	}
	if !dismissInbox(items[0].ID) {
		t.Fatal("dismiss")
	}
	items, _ = inboxSnapshot()
	if len(items) != inboxMax-1 {
		t.Fatalf("after dismiss %d", len(items))
	}
	markInboxSeen()
	_, unseen = inboxSnapshot()
	if unseen != 0 {
		t.Fatal(unseen)
	}
	clearInbox()
	items, unseen = inboxSnapshot()
	if len(items) != 0 || unseen != 0 {
		t.Fatalf("clear %d %d", len(items), unseen)
	}
	if _, err := os.Stat(inboxFile); err != nil {
		t.Fatal(err)
	}
}
