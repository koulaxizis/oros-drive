# orOS Drive for Android

The native Android app of orOS Drive (`online.useoros.drive`): the same
Go core as the desktop app (`mobile/`, bound with gomobile), so the same
vault format and the same rules (VD-1, VD-4, VD-H, mirror Base, trash).

- Sign in with your own Dropbox, Nextcloud (Login Flow v2) or any
  WebDAV server that keeps exact ETags.
- Folders on the phone are backed up to orOS on a schedule (WorkManager).
- The orOS disk shows up in the phone's Files app (DocumentsProvider,
  "orOS Drive"), and the app has its own browser.
- Press and hold a file or folder to keep it offline: it downloads in
  the background and stays up to date; "Offline" lists what is kept.
- Share › orOS Drive from any app: pick a folder, the files upload in
  the background with a progress notification. Nothing is replaced.
- Photos and videos (optional): the phone's media folders are found in
  MediaStore (camera, Screenshots, WhatsApp Images, ...) and each can be
  turned on, with or without what is in it already. The camera (all of
  DCIM) goes to Drive/<phone>/Camera backup, other folders to a subfolder
  of it by name, in batches with one manifest write each (`PutNewMany`).
  Upload only; nothing on the phone is deleted.
- The look follows orOS (style.css, Adwaita skin, light or dark with the
  phone): `Ui.java` builds every screen, laid out around the system bars
  (Android 15 draws apps under them).
- A notification names files kept in both versions after a conflict.

## Build

```sh
# 1. The Go core (needs Go, the Android NDK and Java 17)
go install golang.org/x/mobile/cmd/gomobile golang.org/x/mobile/cmd/gobind
mkdir -p android/app/libs
gomobile bind -target=android/arm64,android/arm,android/amd64 -androidapi 24 \
  -javapkg online.useoros.drive -o android/app/libs/orosdrive.aar ./mobile

# 2. The app (Gradle 8.11)
DRIVE_VERSION=0.4.0 gradle -p android assembleDebug
```

The debug build is `online.useoros.drive.debug` and installs next to
the real app. Release builds are signed with the orOS key by the
koulaxizis/oros Android workflow and published in the orOS F-Droid repo
(F-Droid, Droid-ify, Neo Store, Obtainium all work with it).

No Google Play services, no trackers, no analytics. MIT.
