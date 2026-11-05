# FastTransfer

Send files from your phone to this PC over the same Wi‑Fi. Screenshots for the stores, or any file (video, audio, PDF, docs) up to 4 GB.

## Run

On Windows, double-click `FastTransfer.exe`.

On macOS or Linux:

```bash
go build -o fasttransfer .
./fasttransfer
```

A window opens with the phone link, a QR code, and the files already on this PC. New uploads show up while the window is open. Closing it stops FastTransfer. On macOS and Linux the same window is a Chrome or Edge app when one is installed, and Quit on the page stops it otherwise.

Build the Windows app again with:

```bash
go build -tags bundletools -ldflags="-H windowsgui -s -w" -o FastTransfer.exe .
```

Open the link shown in the window on your phone (same Wi‑Fi). Save folder in the window chooses where files land. Until you pick one, they go to `uploads` next to the app.

mp4, m4v, and webm play directly. Other videos (mkv, avi, mov, wmv, flv, mpeg, ts, 3gp, and mp4 files the browser cannot decode) play inside the window when ffmpeg is on PATH or ffmpeg.exe sits next to FastTransfer.exe. PDF pages are drawn in the window as images. mp3, m4a, wav, flac, and ogg play there too, with the title, artist, and cover stored in the file. Other audio uses ffmpeg the same way.

Windows firewall may ask once — allow it on private networks.

Social media saves a public link with yt-dlp, from the PC sidebar or from the device page. The file lands in the save folder under `Social media\<site>\`, for example `Social media\youtube.com\`. A connected device is offered that file with View and Save. If none is connected, the file stays on this PC and is not scheduled.

Share this one exe. Edge or WebView2 is required. The first launch may extract helper tools next to the exe.

## Updates

Get the latest `FastTransfer.exe` from [Releases](https://github.com/Shaku-Med/fasttransfer/releases/latest). Put it in a folder you own, like Documents, since it keeps its files beside itself.

The app checks this repo for a newer release every few hours and asks before installing it. Each release is signed, and the app refuses any download that does not match that signature, so only releases made with the author's key ever install. You can also check by hand under Connect.

Windows may warn the first time because the exe is not code signed with a paid certificate. Choose More info, then Run anyway.

### Making a release

The tools in `tools/` (ffmpeg.exe and yt-dlp.exe) have to be there, since the release bundles them.

```bash
go run ./cmd/release -genkey
```

Run that once. It saves the signing key in your user folder, outside the repo, and prints the public key that belongs in `updatePubKey` in `updater.go`. Back the key file up. Without it, copies already out there cannot be updated.

```bash
go run ./cmd/release -version 1.2.0
gh release create v1.2.0 release/FastTransfer.exe release/update.json release/update.json.sig --title "FastTransfer 1.2.0" --notes "What changed"
```

The first line builds and signs into `release/`. The second publishes it, and every copy offers the update within a few hours. The tag has to be `v` plus the same version.

## Many phones at once

Every phone that opens the link shows up under Connect with its own name. The phone picks a name like "iPhone 4F2A" and you can rename it from either side. A phone keeps the same entry after a reload or a locked screen, so files and text sent while it was away are waiting when it comes back. Phones that have been away for a day are forgotten.

Send to phone lets you search and tick any number of phones. Each phone holds one open request that the PC answers only when something new arrives, so a room full of idle phones costs almost nothing. The load test drives 1500 simulated phones over real HTTP: a file sent to all of them reaches every one in under 200 ms. Run it with `go test -run TestManyPhones -v`, or set `FT_PHONES` for a different size.

The real limit is Wi-Fi. Most home routers top out somewhere between 30 and 250 connected devices, and every phone downloading a file shares the same airtime, so a big file to a big room takes a while.

## Text between the PC and the phones

Each phone has its own pad with the PC under Text, and both sides see it as they type. Pick Everyone to send one message to every phone at once. Save as clip keeps a snippet in the list next to the pad, where either side can copy it. A clip that is just a link gets an Open button on the phone.

Turn on "Copy clips from the phone straight to this PC's clipboard" and a clip sent from the phone lands on the Windows clipboard right away, even when the window is in the background. The pad and the clips live in memory only, so quitting FastTransfer clears them.

## Shortcuts in the window

Ctrl+1, Ctrl+2 and Ctrl+3 switch between Files, Text and Connect. Ctrl+F searches the open folder, F5 refreshes, Delete removes the selected file, Ctrl+A selects everything, and Ctrl+Enter in the pad saves it as a clip.

## What to pick on the phone

- **Screenshots** → lands in `uploads/iphone|ipad|android|other/` (images only)
- **Any file** → lands in `uploads/files/` (video, audio, PDF, docs, anything)

## Folders

```
uploads/
  iphone/
  ipad/
  android/
  other/
  files/      ← videos and any other file
```
