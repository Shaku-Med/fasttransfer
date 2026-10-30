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

Open the link shown in the window on your phone (same Wi‑Fi). Save folder in the window chooses where files land. Until you pick one, they go to `uploads` next to the app.

mp4, m4v, and webm play directly. Other videos (mkv, avi, mov, wmv, flv, mpeg, ts, 3gp, and mp4 files the browser cannot decode) play inside the window when ffmpeg is on PATH or ffmpeg.exe sits next to FastTransfer.exe. PDF pages are drawn in the window as images. mp3, m4a, wav, flac, and ogg play there too, with the title, artist, and cover stored in the file. Other audio uses ffmpeg the same way.

Windows firewall may ask once — allow it on private networks.

## Text between the PC and the phone

Open Text in the window, or the Text tab on the phone. Both show one shared pad, so whatever you type on one side shows up on the other as you type. Save as clip keeps a snippet in the list below it, where either side can copy it. A clip that is just a link gets an Open button on the phone.

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
