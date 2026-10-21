package online.useoros.drive.app;

import android.content.ContentResolver;
import android.content.Context;
import android.database.Cursor;
import android.net.Uri;
import android.provider.OpenableColumns;

import java.io.File;
import java.io.FileOutputStream;
import java.io.InputStream;
import java.io.OutputStream;
import java.nio.charset.StandardCharsets;
import java.util.ArrayList;
import java.util.Arrays;
import java.util.List;
import java.util.UUID;

/**
 * Files shared to orOS Drive, waiting to be uploaded (wave 4). Each one
 * is copied into the app's own storage at once (the sharing app's grant
 * ends with the share screen) as outbox/<id>/<name> next to a "dest"
 * file naming the disk folder. UploadWorker sends them with PutNew,
 * which never replaces anything, and removes each one once it is in
 * orOS. Nothing on the phone is ever deleted but these copies.
 */
final class Outbox {
    private Outbox() { }

    static File dir(Context c) { return new File(c.getFilesDir(), "outbox"); }

    /** The name the sharing app gives the content, made safe. */
    static String name(ContentResolver cr, Uri u) {
        String n = null;
        try (Cursor q = cr.query(u, new String[]{OpenableColumns.DISPLAY_NAME}, null, null, null)) {
            if (q != null && q.moveToFirst()) n = q.getString(0);
        } catch (Exception ignored) { }
        if (n == null || n.isEmpty()) n = u.getLastPathSegment();
        if (n == null || n.isEmpty()) n = "file";
        n = n.replaceAll("[/\\\\\\x00]", "_").trim();
        if (n.startsWith(".")) n = "_" + n;
        return n.isEmpty() ? "file" : n;
    }

    /** Copies one shared item in; returns its folder (not queued yet). */
    static File take(Context c, InputStream in, String name) throws Exception {
        File d = new File(dir(c), "new-" + UUID.randomUUID());
        if (!d.mkdirs()) throw new Exception("no space");
        try (OutputStream out = new FileOutputStream(new File(d, name))) {
            byte[] buf = new byte[64 * 1024];
            int n;
            while ((n = in.read(buf)) > 0) out.write(buf, 0, n);
        } catch (Exception e) {
            delete(d);
            throw e;
        }
        return d;
    }

    /** Queues taken items for dest (a disk folder). */
    static void queue(List<File> items, String dest) throws Exception {
        for (File d : items) {
            try (OutputStream out = new FileOutputStream(new File(d, "dest"))) {
                out.write(dest.getBytes(StandardCharsets.UTF_8));
            }
            File q = new File(d.getParentFile(), d.getName().substring("new-".length()));
            if (!d.renameTo(q)) throw new Exception("outbox");
        }
    }

    /** Queued items, oldest first; leftovers of a share that was never
     *  finished (no "dest" after a day) are dropped. */
    static List<File> queued(Context c) {
        List<File> out = new ArrayList<>();
        File[] all = dir(c).listFiles();
        if (all == null) return out;
        Arrays.sort(all, (a, b) -> Long.compare(a.lastModified(), b.lastModified()));
        long day = System.currentTimeMillis() - 24L * 3600 * 1000;
        for (File d : all) {
            if (d.getName().startsWith("new-")) {
                if (d.lastModified() < day) delete(d);
            } else if (new File(d, "dest").isFile()) {
                out.add(d);
            }
        }
        return out;
    }

    static String dest(File item) throws Exception {
        try (InputStream in = new java.io.FileInputStream(new File(item, "dest"))) {
            java.io.ByteArrayOutputStream b = new java.io.ByteArrayOutputStream();
            byte[] buf = new byte[1024];
            int n;
            while ((n = in.read(buf)) > 0) b.write(buf, 0, n);
            return new String(b.toByteArray(), StandardCharsets.UTF_8);
        }
    }

    static File content(File item) {
        File[] f = item.listFiles((x, n) -> !"dest".equals(n));
        return f != null && f.length == 1 ? f[0] : null;
    }

    static void delete(File f) {
        File[] kids = f.listFiles();
        if (kids != null) for (File k : kids) delete(k);
        f.delete();
    }
}
