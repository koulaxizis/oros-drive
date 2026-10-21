package online.useoros.drive.app;

import android.Manifest;
import android.app.Notification;
import android.content.ContentResolver;
import android.content.ContentUris;
import android.content.Context;
import android.content.SharedPreferences;
import android.content.pm.PackageManager;
import android.database.Cursor;
import android.net.Uri;
import android.os.Build;
import android.os.Environment;
import android.provider.MediaStore;

import org.json.JSONArray;
import org.json.JSONObject;

import java.io.File;
import java.io.FileOutputStream;
import java.io.InputStream;
import java.io.OutputStream;
import java.util.ArrayList;
import java.util.HashMap;
import java.util.HashSet;
import java.util.List;
import java.util.Map;
import java.util.Set;
import java.util.function.BooleanSupplier;

import online.useoros.drive.mobile.Drive;

/**
 * Photo and video backup (waves 4 and 6). The phone's media folders are
 * found in MediaStore (the camera, Screenshots, WhatsApp Images, ...);
 * the person picks which go, each with or without what is in it
 * already. The camera (all of DCIM) goes to orOS › Drive › <phone> ›
 * Camera backup, every other folder to a subfolder of it by its name.
 * Upload only: nothing on the phone is ever deleted or changed, and
 * PutNewMany never replaces a file in orOS (a retried upload of the
 * same photo writes nothing). Runs in DriveWorker, so under its Wi-Fi
 * and charging options, in batches with one manifest write each. Each
 * folder's "since" (seconds) moves forward only past items that are in
 * orOS or can never go.
 */
final class CameraUploader {
    /** On: at least one folder is chosen. */
    static final String ON = "camera";
    /** The chosen folders (keys); absent with ON = the camera only (wave 4). */
    static final String FOLDERS = "media-folders";
    /** The camera's key: everything in DCIM. */
    static final String CAMERA = "DCIM/";
    // The camera keeps the wave 4 names, so its place is not lost.
    private static final String SINCE = "camera-since";
    private static final String DONE = "camera-done";
    /** Videos above this are left out: the core holds a file in memory. */
    static final long MAX_BYTES = 200L << 20;
    /** A batch: this many files or bytes, whichever comes first. */
    private static final int BATCH_FILES = 25;
    private static final long BATCH_BYTES = 48L << 20;
    private static final String TAG = "media";

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

    private static SharedPreferences prefs(Context c) {
        return c.getSharedPreferences(DriveWorker.PREFS, Context.MODE_PRIVATE);
    }

    private static String sinceKey(String key) { return CAMERA.equals(key) ? SINCE : "since:" + key; }
    private static String doneKey(String key) { return CAMERA.equals(key) ? DONE : "done:" + key; }
    private static String allKey(String key) { return "all:" + key; }

    /** The chosen folders. */
    static Set<String> chosen(Context c) {
        SharedPreferences p = prefs(c);
        if (!p.getBoolean(ON, false)) return new HashSet<>();
        Set<String> s = p.getStringSet(FOLDERS, null);
        if (s == null) {
            Set<String> cam = new HashSet<>();
            cam.add(CAMERA);
            return cam;
        }
        return new HashSet<>(s);
    }

    /** Adds a folder (with what is in it already when older) or drops it. */
    static void choose(Context c, String key, boolean on, boolean older) {
        Set<String> s = chosen(c);
        SharedPreferences.Editor e = prefs(c).edit();
        if (on) {
            s.add(key);
            e.putLong(sinceKey(key), older ? 0 : System.currentTimeMillis() / 1000)
                    .remove(doneKey(key)).putBoolean(allKey(key), older);
        } else {
            s.remove(key);
            e.remove(sinceKey(key)).remove(doneKey(key)).remove(allKey(key));
        }
        e.putStringSet(FOLDERS, s).putBoolean(ON, !s.isEmpty()).apply();
    }

    /** A chosen folder also sends what was in it before it was chosen. */
    static void older(Context c, String key) {
        prefs(c).edit().putLong(sinceKey(key), 0).remove(doneKey(key)).putBoolean(allKey(key), true).apply();
    }

    /** Whether a chosen folder sends everything in it (or only what is new). */
    static boolean all(Context c, String key) { return prefs(c).getBoolean(allKey(key), false); }

    /** One media folder of the phone. */
    static final class Folder {
        String key, name;
        int count, waiting;
        long bytes;
    }

    /** The folder an item is in ("Pictures/Screenshots/"), or null. */
    private static String relative(String rel, String data, String root) {
        if (rel == null && data != null && data.startsWith(root)) {
            int cut = data.lastIndexOf('/');
            rel = cut > root.length() ? data.substring(root.length(), cut + 1) : "";
        }
        if (rel == null) return null;
        if (!rel.isEmpty() && !rel.endsWith("/")) rel += "/";
        // Hidden folders (".thumbnails", ".trash") are not photos to keep.
        for (String seg : rel.split("/")) if (seg.startsWith(".")) return null;
        return rel.startsWith(CAMERA) ? CAMERA : rel;
    }

    private static String root() {
        return Environment.getExternalStorageDirectory().getPath() + "/";
    }

    private static Uri[] collections() {
        return new Uri[]{MediaStore.Images.Media.EXTERNAL_CONTENT_URI, MediaStore.Video.Media.EXTERNAL_CONTENT_URI};
    }

    private static String[] cols() {
        return new String[]{MediaStore.MediaColumns._ID, MediaStore.MediaColumns.DISPLAY_NAME,
                MediaStore.MediaColumns.DATE_ADDED, MediaStore.MediaColumns.SIZE,
                Build.VERSION.SDK_INT >= 29 ? MediaStore.MediaColumns.RELATIVE_PATH : MediaStore.MediaColumns.DATA};
    }

    /** Every folder with photos or videos, the camera first, then by
     *  size of the collection; waiting = not sent yet, for chosen ones. */
    static List<Folder> discover(Context c) {
        Map<String, Folder> by = new HashMap<>();
        Set<String> on = chosen(c);
        SharedPreferences p = prefs(c);
        String root = root();
        boolean q = Build.VERSION.SDK_INT >= 29;
        for (Uri coll : collections()) {
            try (Cursor cur = c.getContentResolver().query(coll, cols(), null, null, null)) {
                while (cur != null && cur.moveToNext()) {
                    String key = relative(q ? cur.getString(4) : null, q ? null : cur.getString(4), root);
                    String name = cur.getString(1);
                    if (key == null || key.isEmpty() || name == null || name.startsWith(".")) continue;
                    Folder f = by.get(key);
                    if (f == null) {
                        f = new Folder();
                        f.key = key;
                        String k = key.substring(0, key.length() - 1);
                        f.name = k.substring(k.lastIndexOf('/') + 1);
                        by.put(key, f);
                    }
                    f.count++;
                    f.bytes += cur.getLong(3);
                    if (on.contains(key) && cur.getLong(2) >= p.getLong(sinceKey(key), Long.MAX_VALUE)
                            && cur.getLong(3) <= MAX_BYTES) f.waiting++;
                }
            } catch (SecurityException ignored) {
                // Access taken back: nothing to show.
            }
        }
        List<Folder> out = new ArrayList<>(by.values());
        out.sort((x, y) -> {
            if (CAMERA.equals(x.key) != CAMERA.equals(y.key)) return CAMERA.equals(x.key) ? -1 : 1;
            return y.count != x.count ? Integer.compare(y.count, x.count) : x.name.compareToIgnoreCase(y.name);
        });
        return out;
    }

    private static final class Item {
        Uri uri; String name; long added; long size;
    }

    /** A folder's items added at or after since, oldest first. */
    private static List<Item> items(ContentResolver cr, String key, long since) {
        List<Item> out = new ArrayList<>();
        boolean q = Build.VERSION.SDK_INT >= 29;
        String root = root();
        String sel;
        String[] args;
        if (q) {
            sel = MediaStore.MediaColumns.DATE_ADDED + " >= ? AND " + MediaStore.MediaColumns.RELATIVE_PATH
                    + (CAMERA.equals(key) ? " LIKE ?" : " = ?");
            args = new String[]{String.valueOf(since), CAMERA.equals(key) ? CAMERA + "%" : key};
        } else {
            // Before 10 there is no RELATIVE_PATH: by file path, the
            // exact folder checked below.
            sel = MediaStore.MediaColumns.DATE_ADDED + " >= ? AND " + MediaStore.MediaColumns.DATA + " LIKE ?";
            args = new String[]{String.valueOf(since), root + key + "%"};
        }
        for (Uri coll : collections()) {
            try (Cursor cur = cr.query(coll, cols(), sel, args, null)) {
                while (cur != null && cur.moveToNext()) {
                    if (!key.equals(relative(q ? cur.getString(4) : null, q ? null : cur.getString(4), root))) continue;
                    Item i = new Item();
                    i.uri = ContentUris.withAppendedId(coll, cur.getLong(0));
                    i.name = cur.getString(1);
                    i.added = cur.getLong(2);
                    i.size = cur.getLong(3);
                    if (i.name != null && !i.name.isEmpty() && !i.name.startsWith(".")) out.add(i);
                }
            }
        }
        out.sort((a, b) -> Long.compare(a.added, b.added));
        return out;
    }

    /** Where a folder goes in orOS. */
    static String dest(String cameraDir, String key) {
        if (CAMERA.equals(key)) return cameraDir;
        String k = key.substring(0, key.length() - 1);
        return cameraDir + "/" + k.substring(k.lastIndexOf('/') + 1);
    }

    /** Uploads what is waiting in every chosen folder; returns how many
     *  went. Stops early (where it is) when stop says so. */
    static int run(Context c, Drive d, BooleanSupplier stop) throws Exception {
        if (!allowed(c)) return 0;
        Set<String> on = chosen(c);
        if (on.isEmpty()) return 0;
        SharedPreferences p = prefs(c);
        ContentResolver cr = c.getContentResolver();
        Map<String, List<Item>> todo = new HashMap<>();
        int total = 0;
        for (String key : on) {
            List<Item> l = items(cr, key, p.getLong(sinceKey(key), System.currentTimeMillis() / 1000));
            todo.put(key, l);
            total += l.size();
        }
        if (total == 0) return 0;
        String cam = d.cameraDir();
        File dir = new File(c.getCacheDir(), "media-upload");
        int[] n = {0, 0}; // sent, handled
        try {
            for (String key : on) {
                if (stop.getAsBoolean()) break;
                folder(c, d, key, todo.get(key), dest(cam, key), dir, stop, n, total);
            }
        } finally {
            Notify.cancel(c, TAG, 1);
            File[] left = dir.listFiles();
            if (left != null) for (File f : left) f.delete();
        }
        return n[0];
    }

    private static void folder(Context c, Drive d, String key, List<Item> all, String dest, File dir,
                               BooleanSupplier stop, int[] n, int total) throws Exception {
        SharedPreferences p = c.getSharedPreferences(DriveWorker.PREFS, Context.MODE_PRIVATE);
        long[] since = {p.getLong(sinceKey(key), 0)};
        Set<String> done = new HashSet<>(p.getStringSet(doneKey(key), new HashSet<>()));
        List<Item> batch = new ArrayList<>();
        List<File> files = new ArrayList<>();
        long bytes = 0;
        ContentResolver cr = c.getContentResolver();
        dir.mkdirs();
        for (int k = 0; k <= all.size(); k++) {
            Item i = k < all.size() ? all.get(k) : null;
            boolean full = batch.size() >= BATCH_FILES || bytes >= BATCH_BYTES;
            if ((i == null || full) && !batch.isEmpty()) {
                if (stop.getAsBoolean()) return;
                Notify.progress(c, TAG, 1, c.getString(R.string.drive_media_title),
                        c.getString(R.string.drive_media_progress, n[1], total), n[1], total);
                try {
                    send(d, key, batch, files, dest, since, done, n);
                } finally {
                    // What went before a failure stays sent.
                    p.edit().putLong(sinceKey(key), since[0]).putStringSet(doneKey(key), new HashSet<>(done)).apply();
                    for (File f : files) if (f != null) f.delete();
                }
                batch.clear();
                files.clear();
                bytes = 0;
            }
            if (i == null) break;
            if (i.added == since[0] && done.contains(i.name)) { n[1]++; continue; }
            File tmp = new File(dir, String.valueOf(files.size()));
            boolean copied = false;
            if (i.size <= MAX_BYTES) {
                try (InputStream in = cr.openInputStream(i.uri); OutputStream out = new FileOutputStream(tmp)) {
                    if (in != null) {
                        byte[] buf = new byte[64 * 1024];
                        int r;
                        while ((r = in.read(buf)) > 0) out.write(buf, 0, r);
                        copied = true;
                    }
                } catch (java.io.FileNotFoundException gone) {
                    // Deleted meanwhile: nothing to send.
                }
            }
            batch.add(i);
            files.add(copied ? tmp : null);
            if (copied) bytes += tmp.length();
        }
    }

    /** Sends one batch. Items that went, or can never go (too big, a
     *  name orOS cannot hold, gone from the phone), move since forward
     *  in order; the first other failure stops there for the next pass. */
    private static void send(Drive d, String key, List<Item> batch, List<File> files, String dest,
                             long[] since, Set<String> done, int[] n) throws Exception {
        JSONArray in = new JSONArray();
        List<Integer> at = new ArrayList<>();
        for (int k = 0; k < batch.size(); k++) {
            if (files.get(k) == null) continue;
            at.add(k);
            in.put(new JSONObject().put("dir", dest).put("name", batch.get(k).name).put("local", files.get(k).getPath()));
        }
        String[] err = new String[batch.size()];
        boolean[] sent = new boolean[batch.size()];
        if (in.length() > 0) {
            JSONArray res = new JSONArray(d.diskPutNewMany(in.toString()));
            for (int r = 0; r < res.length() && r < at.size(); r++) {
                JSONObject o = res.getJSONObject(r);
                if (!o.optString("path").isEmpty()) sent[at.get(r)] = true;
                else err[at.get(r)] = o.optString("error");
            }
        }
        for (int k = 0; k < batch.size(); k++) {
            Item i = batch.get(k);
            String e = err[k];
            boolean never = files.get(k) == null || "too-big".equals(e) || "bad-name".equals(e);
            if (!sent[k] && !never) throw new Exception(e == null || e.isEmpty() ? "not sent" : e);
            if (sent[k]) n[0]++;
            n[1]++;
            // Items added in the same second stay in the window (>=)
            // and are told apart by name; a repeat would write nothing.
            if (i.added != since[0]) { since[0] = i.added; done.clear(); }
            done.add(i.name);
        }
    }

    /** Tells how many went, once a pass sent a good number (a photo or
     *  two every pass is no news). */
    static void told(Context c, int sent) {
        if (sent < 5) return;
        Notify.message(c, TAG, 2, c.getString(R.string.drive_media_title),
                c.getResources().getQuantityString(R.plurals.drive_media_done, sent, sent), Notification.CATEGORY_STATUS);
    }
}
