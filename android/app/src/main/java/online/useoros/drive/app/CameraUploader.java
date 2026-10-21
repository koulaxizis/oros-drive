package online.useoros.drive.app;

import android.Manifest;
import android.content.ContentResolver;
import android.content.ContentUris;
import android.content.Context;
import android.content.SharedPreferences;
import android.content.pm.PackageManager;
import android.database.Cursor;
import android.net.Uri;
import android.os.Build;
import android.provider.MediaStore;

import java.io.File;
import java.io.FileOutputStream;
import java.io.InputStream;
import java.io.OutputStream;
import java.util.ArrayList;
import java.util.List;

import online.useoros.drive.mobile.Drive;

/**
 * Camera backup (wave 4): photos and videos taken after it was turned
 * on (in DCIM, the camera's folder) go to orOS › Drive › <phone> ›
 * Camera backup. Upload only: nothing on the phone is ever deleted or changed,
 * and PutNew never replaces a file in orOS (a retried upload of the
 * same photo writes nothing). Runs in DriveWorker, so under its Wi-Fi
 * and charging options. "camera-since" (seconds) moves forward only
 * past items that are in orOS or can never go.
 */
final class CameraUploader {
    static final String ON = "camera";
    static final String SINCE = "camera-since";
    /** Names already sent from the second "camera-since" names. */
    static final String DONE = "camera-done";
    /** Videos above this are left out: the core holds a file in memory. */
    static final long MAX_BYTES = 200L << 20;

    private CameraUploader() { }

    static String[] permissions() {
        if (Build.VERSION.SDK_INT >= 33) {
            return new String[]{Manifest.permission.READ_MEDIA_IMAGES, Manifest.permission.READ_MEDIA_VIDEO};
        }
        return new String[]{Manifest.permission.READ_EXTERNAL_STORAGE};
    }

    static boolean allowed(Context c) {
        boolean any = false;
        for (String p : permissions()) any |= c.checkSelfPermission(p) == PackageManager.PERMISSION_GRANTED;
        if (Build.VERSION.SDK_INT >= 34) {
            // Android 14 "selected photos only": just those.
            any |= c.checkSelfPermission("android.permission.READ_MEDIA_VISUAL_USER_SELECTED") == PackageManager.PERMISSION_GRANTED;
        }
        return any;
    }

    private static final class Item {
        Uri uri; String name; long added; long size;
    }

    private static List<Item> since(ContentResolver cr, Uri coll, long since) {
        List<Item> out = new ArrayList<>();
        String sel;
        String[] args;
        if (Build.VERSION.SDK_INT >= 29) {
            sel = MediaStore.MediaColumns.DATE_ADDED + " >= ? AND " + MediaStore.MediaColumns.RELATIVE_PATH + " LIKE ?";
            args = new String[]{String.valueOf(since), "DCIM/%"};
        } else {
            sel = MediaStore.MediaColumns.DATE_ADDED + " >= ? AND " + MediaStore.MediaColumns.DATA + " LIKE ?";
            args = new String[]{String.valueOf(since), "%/DCIM/%"};
        }
        String[] cols = {MediaStore.MediaColumns._ID, MediaStore.MediaColumns.DISPLAY_NAME,
                MediaStore.MediaColumns.DATE_ADDED, MediaStore.MediaColumns.SIZE};
        try (Cursor q = cr.query(coll, cols, sel, args, MediaStore.MediaColumns.DATE_ADDED + " ASC")) {
            while (q != null && q.moveToNext()) {
                Item i = new Item();
                i.uri = ContentUris.withAppendedId(coll, q.getLong(0));
                i.name = q.getString(1);
                i.added = q.getLong(2);
                i.size = q.getLong(3);
                if (i.name != null && !i.name.isEmpty() && !i.name.startsWith(".")) out.add(i);
            }
        }
        return out;
    }

    /** Uploads what is new; returns how many went. */
    static int run(Context c, Drive d) throws Exception {
        SharedPreferences p = c.getSharedPreferences(DriveWorker.PREFS, Context.MODE_PRIVATE);
        if (!p.getBoolean(ON, false) || !allowed(c)) return 0;
        long since = p.getLong(SINCE, System.currentTimeMillis() / 1000);
        ContentResolver cr = c.getContentResolver();
        List<Item> all = new ArrayList<>();
        all.addAll(since(cr, MediaStore.Images.Media.EXTERNAL_CONTENT_URI, since));
        all.addAll(since(cr, MediaStore.Video.Media.EXTERNAL_CONTENT_URI, since));
        all.sort((a, b) -> Long.compare(a.added, b.added));
        if (all.isEmpty()) return 0;
        java.util.Set<String> done = new java.util.HashSet<>(p.getStringSet(DONE, new java.util.HashSet<>()));
        String dest = d.cameraDir();
        File tmp = new File(c.getCacheDir(), "camera-upload");
        int n = 0;
        try {
            for (Item i : all) {
                if (i.added == since && done.contains(i.name)) continue;
                if (i.size <= MAX_BYTES) {
                    try (InputStream in = cr.openInputStream(i.uri); OutputStream out = new FileOutputStream(tmp)) {
                        if (in == null) continue;  // gone meanwhile: try next pass
                        byte[] buf = new byte[64 * 1024];
                        int k;
                        while ((k = in.read(buf)) > 0) out.write(buf, 0, k);
                    }
                    try {
                        d.diskPutNew(dest, i.name, tmp.getPath());
                        n++;
                    } catch (Exception e) {
                        String m = String.valueOf(e.getMessage());
                        // Anything but "can never go" stops here; the
                        // next pass starts again from this item.
                        if (!"too-big".equals(m) && !"bad-name".equals(m)) throw e;
                    }
                }
                // Items added in the same second stay in the window (>=)
                // and are told apart by name; a repeat would write nothing.
                if (i.added != since) { since = i.added; done.clear(); }
                done.add(i.name);
                p.edit().putLong(SINCE, since).putStringSet(DONE, new java.util.HashSet<>(done)).apply();
            }
        } finally {
            tmp.delete();
        }
        return n;
    }
}
