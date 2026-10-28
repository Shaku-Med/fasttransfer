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
go build -ldflags="-H windowsgui -s -w" -o FastTransfer.exe .
```

Open the link shown in the window on your phone (same Wi‑Fi).

mp4, m4v, and webm play directly. Other videos (mkv, avi, mov, wmv, flv, mpeg, ts, 3gp, and mp4 files the browser cannot decode) play inside the window when ffmpeg is on PATH or ffmpeg.exe sits next to FastTransfer.exe. PDF pages are drawn in the window as images. mp3, m4a, wav, flac, and ogg play there too, with the title, artist, and cover stored in the file. Other audio uses ffmpeg the same way.

Windows firewall may ask once — allow it on private networks.

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
