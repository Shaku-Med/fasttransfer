package main

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const saveFolderFile = "save-folder.txt"

var (
	rootMu     sync.RWMutex
	uploadRoot string
)

func saveRoot() string {
	rootMu.RLock()
	defer rootMu.RUnlock()
	return uploadRoot
}

func setRootPath(path string) {
	rootMu.Lock()
	uploadRoot = path
	rootMu.Unlock()
}

func defaultSaveRoot() string {
	return filepath.Join(appDir, "uploads")
}

func initSaveRoot() error {
	if chosen := readSavedFolder(); chosen != "" {
		if err := ensureSaveDirs(chosen); err == nil {
			setRootPath(chosen)
			return nil
		}
	}
	def := defaultSaveRoot()
	if err := os.MkdirAll(def, 0o755); err != nil {
		return err
	}
	if err := ensureSaveDirs(def); err != nil {
		return err
	}
	setRootPath(def)
	return nil
}

func readSavedFolder() string {
	raw, err := os.ReadFile(filepath.Join(appDir, saveFolderFile))
	if err != nil {
		return ""
	}
	path := strings.TrimSpace(string(raw))
	if path == "" {
		return ""
	}
	full, err := filepath.Abs(path)
	if err != nil {
		return ""
	}
	info, err := os.Stat(full)
	if err != nil || !info.IsDir() {
		return ""
	}
	return full
}

func ensureSaveDirs(root string) error {
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return errNoFolder
	}
	for name := range destinations {
		if err := os.MkdirAll(filepath.Join(root, name), 0o755); err != nil {
			return errFolderUse
		}
	}
	return nil
}

var (
	errNoFolder  = errors.New("Choose a folder that exists.")
	errFolderUse = errors.New("That folder could not be used.")
)

func applySaveRoot(path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return errNoFolder
	}
	full, err := filepath.Abs(path)
	if err != nil {
		return errNoFolder
	}
	if err := ensureSaveDirs(full); err != nil {
		if errors.Is(err, errFolderUse) {
			return err
		}
		return errNoFolder
	}
	dest := filepath.Join(appDir, saveFolderFile)
	tmp := dest + ".tmp"
	if err := os.WriteFile(tmp, []byte(full+"\n"), 0o600); err != nil {
		return errFolderUse
	}
	if err := os.Rename(tmp, dest); err != nil {
		_ = os.Remove(tmp)
		return errFolderUse
	}
	setRootPath(full)
	return nil
}

func rootLabel() string {
	root := saveRoot()
	name := filepath.Base(root)
	if name == "" || name == "." || name == string(os.PathSeparator) {
		return root
	}
	return name
}

func (s *server) setRoot(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string `json:"path"`
	}
	if !readBody(w, r, &req) {
		return
	}
	if err := applySaveRoot(req.Path); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	root := saveRoot()
	writeJSON(w, http.StatusOK, map[string]string{"path": root, "name": rootLabel()})
}
