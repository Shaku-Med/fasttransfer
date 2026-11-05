// Command release builds FastTransfer for sharing and signs it so installed
// copies trust it as an update.
//
//	go run ./cmd/release -genkey        once, makes the signing key
//	go run ./cmd/release -version 1.2.0 builds and signs into release/
//
// The private key lives outside the repo, in the user's profile. Lose it and
// installed copies can no longer be updated, so keep a backup somewhere safe.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

var versionRe = regexp.MustCompile(`^(0|[1-9][0-9]{0,3})\.(0|[1-9][0-9]{0,3})\.(0|[1-9][0-9]{0,3})$`)

type manifest struct {
	App     string `json:"app"`
	Version string `json:"version"`
	File    string `json:"file"`
	Size    int64  `json:"size"`
	SHA256  string `json:"sha256"`
}

func main() {
	home, err := os.UserHomeDir()
	if err != nil {
		fail(err)
	}
	keyPath := flag.String("key", filepath.Join(home, ".fasttransfer", "release-key"), "signing key file")
	genkey := flag.Bool("genkey", false, "make a new signing key")
	version := flag.String("version", "", "version to build, like 1.2.0")
	flag.Parse()

	if *genkey {
		if err := makeKey(*keyPath); err != nil {
			fail(err)
		}
		return
	}
	if !versionRe.MatchString(*version) {
		fail(errors.New("give -version as three numbers, like 1.2.0"))
	}
	priv, err := readKey(*keyPath)
	if err != nil {
		fail(err)
	}
	pub := base64.StdEncoding.EncodeToString(priv.Public().(ed25519.PublicKey))
	src, err := os.ReadFile("updater.go")
	if err != nil {
		fail(errors.New("run this from the FastTransfer folder"))
	}
	if !strings.Contains(string(src), `"`+pub+`"`) {
		fail(errors.New("updater.go does not hold this key's public half, so installed copies would refuse the release"))
	}

	out := "release"
	if err := os.MkdirAll(out, 0o755); err != nil {
		fail(err)
	}
	exe := filepath.Join(out, "FastTransfer.exe")
	build := exec.Command("go", "build", "-tags", "bundletools", "-trimpath",
		"-ldflags", "-H windowsgui -s -w -X main.appVersion="+*version, "-o", exe, ".")
	build.Env = append(os.Environ(), "GOOS=windows", "GOARCH=amd64", "CGO_ENABLED=0")
	build.Stdout, build.Stderr = os.Stdout, os.Stderr
	if err := build.Run(); err != nil {
		fail(err)
	}

	size, sum, err := hashFile(exe)
	if err != nil {
		fail(err)
	}
	m := manifest{App: "fasttransfer", Version: *version, File: "FastTransfer.exe", Size: size, SHA256: sum}
	body, err := json.Marshal(m)
	if err != nil {
		fail(err)
	}
	sig := base64.StdEncoding.EncodeToString(ed25519.Sign(priv, body))
	if err := os.WriteFile(filepath.Join(out, "update.json"), body, 0o644); err != nil {
		fail(err)
	}
	if err := os.WriteFile(filepath.Join(out, "update.json.sig"), []byte(sig), 0o644); err != nil {
		fail(err)
	}
	fmt.Printf("Built and signed %s (%.1f MB)\nUpload release/FastTransfer.exe, release/update.json and release/update.json.sig to a GitHub release tagged v%s\n", exe, float64(size)/(1<<20), *version)
}

func makeKey(path string) error {
	if _, err := os.Stat(path); err == nil {
		return errors.New("a key already exists at " + path + ", delete it first if you really mean to replace it")
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(base64.StdEncoding.EncodeToString(priv)), 0o600); err != nil {
		return err
	}
	fmt.Printf("Saved the signing key to %s\nKeep a backup of it somewhere safe and never commit it.\nPut this public key in updater.go:\n%s\n", path, base64.StdEncoding.EncodeToString(pub))
	return nil
}

func readKey(path string) (ed25519.PrivateKey, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, errors.New("no signing key at " + path + ", run with -genkey first")
	}
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil || len(key) != ed25519.PrivateKeySize {
		return nil, errors.New("the signing key at " + path + " is damaged")
	}
	return ed25519.PrivateKey(key), nil
}

func hashFile(path string) (int64, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, "", err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return 0, "", err
	}
	return n, hex.EncodeToString(h.Sum(nil)), nil
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "release:", err)
	os.Exit(1)
}
