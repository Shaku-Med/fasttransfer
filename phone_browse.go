package main

import (
	"archive/zip"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type phoneEntry struct {
	Name  string `json:"name"`
	Label string `json:"label"`
	Rel   string `json:"rel"`
	Dir   bool   `json:"dir"`
	Size  int64  `json:"size"`
}

func (s *server) phoneBrowse(w http.ResponseWriter, r *http.Request) {
	rel, err := parseRel(r.URL.Query().Get("path"))
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "that folder is not available"})
		return
	}
	dir, err := absInside(rel)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "that folder is not available"})
		return
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "that folder is not available"})
		return
	}
	rows := []phoneEntry{}
	entries, err := os.ReadDir(dir)
	if err == nil {
		for _, entry := range entries {
			name := entry.Name()
			if !plainFileName(name) {
				continue
			}
			childRel := name
			if rel != "" {
				childRel = rel + "/" + name
			}
			childAbs, err := absInside(childRel)
			if err != nil {
				continue
			}
			isDir := entry.IsDir()
			var size int64
			if st, err := os.Stat(childAbs); err == nil {
				isDir = st.IsDir()
				if !isDir && !st.Mode().IsRegular() {
					continue
				}
				size = st.Size()
			} else if !isDir {
				continue
			}
			rows = append(rows, phoneEntry{
				Name:  name,
				Label: displayName(name, isDir),
				Rel:   childRel,
				Dir:   isDir,
				Size:  size,
			})
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Dir != rows[j].Dir {
			return rows[i].Dir
		}
		return strings.ToLower(rows[i].Label) < strings.ToLower(rows[j].Label)
	})
	label := rootLabel()
	if rel != "" {
		label = displayName(filepath.Base(rel), true)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"rel":     rel,
		"name":    label,
		"crumbs":  crumbsFor(rel),
		"entries": rows,
	})
}

func (s *server) phoneSavedGet(w http.ResponseWriter, r *http.Request) {
	full, info, name, err := regularInside(r.URL.Query().Get("path"))
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "that file is not available"})
		return
	}
	f, err := os.Open(full)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "that file is not available"})
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", attachmentDisposition(name))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'")
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, r, name, info.ModTime(), f)
}

func (s *server) phonePack(w http.ResponseWriter, r *http.Request) {
	rel, err := parseRel(r.URL.Query().Get("path"))
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "that folder is not available"})
		return
	}
	dir, err := absInside(rel)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "that folder is not available"})
		return
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "that folder is not available"})
		return
	}
	label := rootLabel()
	if rel != "" {
		label = displayName(filepath.Base(rel), true)
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", attachmentDisposition(label+".zip"))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'")
	w.Header().Set("Cache-Control", "no-store")
	zw := zip.NewWriter(w)
	err = filepath.WalkDir(dir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if path == dir {
			return nil
		}
		relName, err := filepath.Rel(dir, path)
		if err != nil || relName == "." || strings.HasPrefix(relName, "..") {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		name := filepath.ToSlash(relName)
		if name == "" || strings.Contains(name, "../") || strings.HasPrefix(name, "/") {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			target, err := filepath.EvalSymlinks(path)
			if err != nil || !insideUpload(target) {
				return nil
			}
			st, err := os.Stat(target)
			if err != nil || st.IsDir() || !st.Mode().IsRegular() {
				return nil
			}
			return addZipFile(zw, name, target, st)
		}
		if d.IsDir() {
			if _, err := absInside(joinRel(rel, name)); err != nil {
				return fs.SkipDir
			}
			hdr := &zip.FileHeader{Name: name + "/", Method: zip.Store}
			hdr.SetMode(0o755)
			_, err := zw.CreateHeader(hdr)
			return err
		}
		st, err := os.Stat(path)
		if err != nil || !st.Mode().IsRegular() || !insideUpload(path) {
			return nil
		}
		return addZipFile(zw, name, path, st)
	})
	closeErr := zw.Close()
	if err == nil {
		err = closeErr
	}
	_ = err
}

func joinRel(parent, name string) string {
	if parent == "" {
		return name
	}
	return parent + "/" + name
}

func addZipFile(zw *zip.Writer, name, path string, info os.FileInfo) error {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	hdr, err := zip.FileInfoHeader(info)
	if err != nil {
		return nil
	}
	hdr.Name = name
	hdr.Method = zip.Store
	dst, err := zw.CreateHeader(hdr)
	if err != nil {
		return err
	}
	_, err = io.Copy(dst, f)
	return err
}
