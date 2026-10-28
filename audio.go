package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/dhowden/tag"
)

var directAudio = map[string]bool{
	".mp3": true, ".wav": true, ".m4a": true, ".ogg": true, ".flac": true,
}

func (s *server) audioInfo(w http.ResponseWriter, r *http.Request) {
	full, _, name, err := regularInside(r.URL.Query().Get("path"))
	ext := strings.ToLower(filepath.Ext(name))
	if err != nil || audioTypes[ext] == "" {
		http.NotFound(w, r)
		return
	}
	meta := readAudioTags(full)
	mode := "direct"
	if !directAudio[ext] || r.URL.Query().Get("force") == "1" {
		if findBin("ffmpeg") == "" {
			mode = "need-ffmpeg"
		} else {
			mode = "ffmpeg"
		}
	}
	duration := probeDuration(full)
	rate, bitrate := probeAudio(full)
	writeJSON(w, http.StatusOK, map[string]any{
		"mode":        mode,
		"title":       meta.title,
		"artist":      meta.artist,
		"album":       meta.album,
		"albumArtist": meta.albumArtist,
		"track":       meta.track,
		"tracks":      meta.tracks,
		"year":        meta.year,
		"genre":       meta.genre,
		"duration":    duration,
		"bitrate":     bitrate,
		"sampleRate":  rate,
		"hasArt":      meta.art,
	})
}

func (s *server) audioCover(w http.ResponseWriter, r *http.Request) {
	full, _, name, err := regularInside(r.URL.Query().Get("path"))
	if err != nil || audioTypes[strings.ToLower(filepath.Ext(name))] == "" {
		http.NotFound(w, r)
		return
	}
	pic := audioPicture(full)
	if pic == nil {
		http.NotFound(w, r)
		return
	}
	ctype := sniffImage(pic)
	if ctype == "" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'")
	w.Header().Set("Cache-Control", "private, max-age=300")
	_, _ = w.Write(pic)
}

type audioTags struct {
	title, artist, album, albumArtist, genre string
	track, tracks, year                      int
	art                                      bool
}

func readAudioTags(path string) audioTags {
	f, err := os.Open(path)
	if err != nil {
		return audioTags{}
	}
	defer f.Close()
	m, err := tag.ReadFrom(f)
	if err != nil || m == nil {
		return audioTags{}
	}
	track, tracks := m.Track()
	pic := m.Picture()
	return audioTags{
		title:       strings.TrimSpace(m.Title()),
		artist:      strings.TrimSpace(m.Artist()),
		album:       strings.TrimSpace(m.Album()),
		albumArtist: strings.TrimSpace(m.AlbumArtist()),
		genre:       strings.TrimSpace(m.Genre()),
		year:        m.Year(),
		track:       track,
		tracks:      tracks,
		art:         pic != nil && len(pic.Data) > 32 && sniffImage(pic.Data) != "",
	}
}

func audioPicture(path string) []byte {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	m, err := tag.ReadFrom(f)
	if err != nil || m == nil || m.Picture() == nil {
		return nil
	}
	data := m.Picture().Data
	if len(data) < 32 || len(data) > 8<<20 || sniffImage(data) == "" {
		return nil
	}
	return data
}

func sniffImage(b []byte) string {
	if len(b) >= 3 && b[0] == 0xFF && b[1] == 0xD8 && b[2] == 0xFF {
		return "image/jpeg"
	}
	if len(b) >= 8 && bytes.Equal(b[:8], []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}) {
		return "image/png"
	}
	if len(b) >= 6 && (bytes.HasPrefix(b, []byte("GIF87a")) || bytes.HasPrefix(b, []byte("GIF89a"))) {
		return "image/gif"
	}
	if len(b) >= 12 && bytes.Equal(b[:4], []byte("RIFF")) && bytes.Equal(b[8:12], []byte("WEBP")) {
		return "image/webp"
	}
	if len(b) >= 2 && b[0] == 'B' && b[1] == 'M' {
		return "image/bmp"
	}
	return ""
}

func probeAudio(path string) (sample, bitrate int) {
	p := findBin("ffprobe")
	if p == "" {
		return 0, 0
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, p, "-v", "error", "-select_streams", "a:0", "-show_entries", "stream=sample_rate,bit_rate", "-of", "default=noprint_wrappers=1", path)
	hideConsole(cmd)
	out, err := cmd.Output()
	if err != nil {
		return 0, 0
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "sample_rate=") {
			sample, _ = strconv.Atoi(strings.TrimPrefix(line, "sample_rate="))
		}
		if strings.HasPrefix(line, "bit_rate=") {
			bitrate, _ = strconv.Atoi(strings.TrimPrefix(line, "bit_rate="))
		}
	}
	if sample < 0 || sample > 384000 {
		sample = 0
	}
	if bitrate < 0 || bitrate > 10000000 {
		bitrate = 0
	}
	return sample, bitrate
}

func streamAudio(w http.ResponseWriter, r *http.Request, full string) {
	if findBin("ffmpeg") == "" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusUnsupportedMediaType)
		_, _ = w.Write([]byte("This audio needs ffmpeg"))
		return
	}
	start := streamStart(r.URL.Query().Get("ss"))
	first, rest, cleanup, err := startFFmpeg(r.Context(), audioArgs(full, start))
	if err != nil {
		if r.Context().Err() != nil {
			return
		}
		http.Error(w, "could not play this audio", http.StatusBadGateway)
		return
	}
	writeFFmpeg(w, r, first, rest, cleanup, "audio/mp4")
}

func audioArgs(path string, start float64) []string {
	args := []string{
		"-hide_banner", "-loglevel", "error", "-nostdin",
		"-protocol_whitelist", "file,crypto,pipe",
	}
	if start > 0 {
		args = append(args, "-ss", strconv.FormatFloat(start, 'f', 3, 64))
	}
	args = append(args,
		"-i", path, "-vn", "-map", "0:a:0",
		"-c:a", "aac", "-b:a", "160k", "-ac", "2",
		"-movflags", "frag_keyframe+empty_moov+default_base_moof",
		"-f", "mp4", "pipe:1",
	)
	return args
}

func writeFFmpeg(w http.ResponseWriter, r *http.Request, first []byte, rest io.Reader, cleanup func(), contentType string) {
	defer cleanup()
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", "inline")
	w.Header().Set("Content-Security-Policy", "default-src 'none'")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	if _, err := w.Write(first); err != nil {
		return
	}
	if flusher != nil {
		flusher.Flush()
	}
	buf := make([]byte, 32*1024)
	ctx := r.Context()
	for {
		if ctx.Err() != nil {
			return
		}
		n, rerr := rest.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if rerr != nil {
			return
		}
	}
}
