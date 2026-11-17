package main

import (
	"bufio"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

// Sites often hand out VP9, AV1 or Opus, which iPhones, many TVs and older
// Windows players cannot open. Every saved video ends up as H.264 with AAC in
// MP4 and every saved song as AAC in M4A, which play on every phone.

var (
	probeVideoRe = regexp.MustCompile(`Stream #\S+.*?: Video: (\w+)`)
	probeAudioRe = regexp.MustCompile(`Stream #\S+.*?: Audio: (\w+)`)
	probeDurRe   = regexp.MustCompile(`Duration: (\d+):(\d\d):(\d\d(?:\.\d+)?)`)
	ffTimeRe     = regexp.MustCompile(`^out_time_us=(\d+)$`)

	encoderMu    sync.Mutex
	encoderCache = map[string]string{}
)

type mediaProbe struct {
	video, audio string
	seconds      float64
}

func probeMedia(ctx context.Context, ff, file string) mediaProbe {
	cmd := exec.CommandContext(ctx, ff, "-hide_banner", "-i", file)
	hideConsole(cmd)
	out, _ := cmd.CombinedOutput() // ffmpeg exits 1 when given no output; the stream list is still printed
	var p mediaProbe
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, "attached pic") {
			continue
		}
		if m := probeVideoRe.FindStringSubmatch(line); m != nil && p.video == "" {
			p.video = strings.ToLower(m[1])
		}
		if m := probeAudioRe.FindStringSubmatch(line); m != nil && p.audio == "" {
			p.audio = strings.ToLower(m[1])
		}
		if m := probeDurRe.FindStringSubmatch(line); m != nil && p.seconds == 0 {
			h, _ := strconv.Atoi(m[1])
			mi, _ := strconv.Atoi(m[2])
			s, _ := strconv.ParseFloat(m[3], 64)
			p.seconds = float64(h*3600+mi*60) + s
		}
	}
	return p
}

// h264Encoder finds an H.264 encoder this ffmpeg has. Fedora and some other
// distros ship ffmpeg without x264 but with Cisco's OpenH264.
func h264Encoder(ff string) string {
	encoderMu.Lock()
	defer encoderMu.Unlock()
	if enc, ok := encoderCache[ff]; ok {
		return enc
	}
	cmd := exec.Command(ff, "-hide_banner", "-encoders")
	hideConsole(cmd)
	out, _ := cmd.Output()
	enc := ""
	for _, name := range []string{"libx264", "libopenh264"} {
		if strings.Contains(string(out), " "+name+" ") {
			enc = name
			break
		}
	}
	encoderCache[ff] = enc
	return enc
}

// playablePlan says what ffmpeg has to do, or nil when the file already
// plays everywhere.
func playablePlan(p mediaProbe, ext, encoder string) (args []string, outExt string) {
	goodAudio := p.audio == "" || p.audio == "aac" || p.audio == "mp3"
	if p.video == "" {
		if p.audio == "" || goodAudio && (ext == ".m4a" || ext == ".mp3") {
			return nil, ""
		}
		if goodAudio {
			return []string{"-vn", "-c:a", "copy"}, ".m4a"
		}
		return []string{"-vn", "-c:a", "aac", "-b:a", "192k"}, ".m4a"
	}
	audio := []string{"-c:a", "copy"}
	if !goodAudio {
		audio = []string{"-c:a", "aac", "-b:a", "160k"}
	}
	if p.video == "h264" {
		if goodAudio && (ext == ".mp4" || ext == ".m4v" || ext == ".mov") {
			return nil, ""
		}
		return append([]string{"-map", "0:v:0", "-map", "0:a:0?", "-c:v", "copy"}, audio...), ".mp4"
	}
	video := []string{"-c:v", encoder, "-pix_fmt", "yuv420p", "-vf", "scale=trunc(iw/2)*2:trunc(ih/2)*2"}
	switch encoder {
	case "libx264":
		video = append(video, "-preset", "veryfast", "-crf", "21", "-profile:v", "high")
	case "libopenh264":
		video = append(video, "-b:v", "5M")
	default:
		return nil, ""
	}
	if goodAudio && p.audio == "" {
		audio = nil
	}
	return append(append([]string{"-map", "0:v:0", "-map", "0:a:0?"}, video...), audio...), ".mp4"
}

// makePlayable converts name inside dir when it would not play everywhere
// and returns the name of the file to keep. When conversion is not possible
// the original stays, since a file that plays somewhere beats no file.
func makePlayable(ctx context.Context, ff, dir, name string, progress func(pct float64), started func(*exec.Cmd)) (string, error) {
	src := filepath.Join(dir, name)
	p := probeMedia(ctx, ff, src)
	ext := strings.ToLower(filepath.Ext(name))
	args, outExt := playablePlan(p, ext, h264Encoder(ff))
	if args == nil {
		return name, nil
	}
	outName := strings.TrimSuffix(name, filepath.Ext(name)) + outExt
	tmp := filepath.Join(dir, ".converting-"+outName)
	_ = os.Remove(tmp)
	full := append([]string{"-hide_banner", "-nostdin", "-y", "-i", src}, args...)
	full = append(full, "-movflags", "+faststart", "-progress", "pipe:1", "-nostats", "-f", "mp4", tmp)
	cmd := exec.CommandContext(ctx, ff, full...)
	hideConsole(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return name, err
	}
	if err := cmd.Start(); err != nil {
		return name, err
	}
	started(cmd)
	sc := bufio.NewScanner(stdout)
	for sc.Scan() {
		if m := ffTimeRe.FindStringSubmatch(strings.TrimSpace(sc.Text())); m != nil && p.seconds > 0 {
			us, _ := strconv.ParseFloat(m[1], 64)
			progress(min(99, us/1e4/p.seconds))
		}
	}
	if err := cmd.Wait(); err != nil {
		_ = os.Remove(tmp)
		return name, errors.New("could not convert the file")
	}
	if outName != name {
		if _, err := os.Stat(filepath.Join(dir, outName)); err == nil {
			_ = os.Remove(tmp)
			return name, errors.New("a file with the converted name already exists")
		}
	}
	if err := os.Rename(tmp, filepath.Join(dir, outName)); err != nil {
		_ = os.Remove(tmp)
		return name, err
	}
	if outName != name {
		_ = os.Remove(src)
	}
	return outName, nil
}
