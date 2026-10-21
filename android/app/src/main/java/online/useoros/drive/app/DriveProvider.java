package online.useoros.drive.app;

import android.app.Notification;
import android.app.NotificationChannel;
import android.app.NotificationManager;
import android.app.PendingIntent;
import android.content.Context;
import android.content.Intent;
import android.database.Cursor;
import android.database.MatrixCursor;
import android.net.Uri;
import android.os.Build;
import android.os.CancellationSignal;
import android.os.Handler;
import android.os.HandlerThread;
import android.os.ParcelFileDescriptor;
import android.provider.DocumentsContract;
import android.provider.DocumentsContract.Document;
import android.provider.DocumentsContract.Root;
import android.provider.DocumentsProvider;
import android.util.Log;
import android.webkit.MimeTypeMap;

import org.json.JSONArray;
import org.json.JSONObject;

import java.io.File;
import java.io.FileInputStream;
import java.io.FileNotFoundException;
import java.io.FileOutputStream;
import java.io.InputStream;
import java.io.OutputStream;
import java.text.SimpleDateFormat;
import java.util.Date;
import java.util.Locale;
import java.util.TimeZone;

import online.useoros.drive.mobile.Drive;

/**
 * The whole orOS disk as "orOS" in Android's Files app and in every
 * file picker (DRIVE-A, wave 5). Document ids are disk paths
 * ("/internal/Docs/a.txt"). The Go core (vdisk) does the work: a file
 * downloads when an app opens it and uploads when the app closes it
 * after writing; a file changed elsewhere meanwhile is kept beside it
 * as a conflict copy. Shown only while signed in with the vault
 * unlocked.
 */
public class DriveProvider extends DocumentsProvider {
    private static final String TAG = "orOS/DriveProvider";
    private static final String ROOT = "/internal";
    private static final String[] ROOT_COLS = {Root.COLUMN_ROOT_ID, Root.COLUMN_FLAGS, Root.COLUMN_TITLE,
            Root.COLUMN_SUMMARY, Root.COLUMN_DOCUMENT_ID, Root.COLUMN_ICON, Root.COLUMN_MIME_TYPES};
    private static final String[] DOC_COLS = {Document.COLUMN_DOCUMENT_ID, Document.COLUMN_DISPLAY_NAME,
            Document.COLUMN_MIME_TYPE, Document.COLUMN_SIZE, Document.COLUMN_LAST_MODIFIED, Document.COLUMN_FLAGS};

    private Handler closer;

    static String authority(Context c) { return c.getPackageName() + ".drive"; }

    /** Tells the Files app to show or hide the root (after sign-in,
     *  unlock, lock or sign-out). */
    static void rootsChanged(Context c) {
        c.getContentResolver().notifyChange(DocumentsContract.buildRootsUri(authority(c)), null);
    }

    @Override
    public boolean onCreate() {
        // The Go core loads on first use, not with every app start.
        HandlerThread t = new HandlerThread("orOS Drive uploads");
        t.start();
        closer = new Handler(t.getLooper());
        return true;
    }

    private Drive drive() throws FileNotFoundException {
        try {
            return DriveCore.get(getContext());
        } catch (Exception e) {
            throw new FileNotFoundException(String.valueOf(e.getMessage()));
        }
    }

    private boolean ready() {
        if (!DriveCore.everSignedIn(getContext())) return false;
        try {
            JSONObject s = new JSONObject(drive().status());
            return s.optBoolean("signedIn") && s.optBoolean("unlocked");
        } catch (Exception e) {
            return false;
        }
    }

    // ---------- roots and documents ----------

    @Override
    public Cursor queryRoots(String[] projection) {
        MatrixCursor c = new MatrixCursor(projection != null ? projection : ROOT_COLS);
        c.setNotificationUri(getContext().getContentResolver(), DocumentsContract.buildRootsUri(authority(getContext())));
        if (!ready()) return c;
        MatrixCursor.RowBuilder r = c.newRow();
        r.add(Root.COLUMN_ROOT_ID, "oros");
        r.add(Root.COLUMN_FLAGS, Root.FLAG_SUPPORTS_CREATE | Root.FLAG_SUPPORTS_IS_CHILD);
        r.add(Root.COLUMN_TITLE, "orOS");
        r.add(Root.COLUMN_SUMMARY, getContext().getString(R.string.drive_files_summary));
        r.add(Root.COLUMN_DOCUMENT_ID, ROOT);
        r.add(Root.COLUMN_ICON, R.mipmap.ic_launcher);
        r.add(Root.COLUMN_MIME_TYPES, "*/*");
        return c;
    }

    @Override
    public Cursor queryDocument(String documentId, String[] projection) throws FileNotFoundException {
        MatrixCursor c = new MatrixCursor(projection != null ? projection : DOC_COLS);
        addRow(c, stat(documentId));
        return c;
    }

    @Override
    public Cursor queryChildDocuments(String parentId, String[] projection, String sortOrder) throws FileNotFoundException {
        MatrixCursor c = new MatrixCursor(projection != null ? projection : DOC_COLS);
        try {
            JSONArray list = new JSONArray(drive().diskList(check(parentId)));
            for (int i = 0; i < list.length(); i++) addRow(c, list.getJSONObject(i));
        } catch (FileNotFoundException e) {
            throw e;
        } catch (Exception e) {
            throw notFound(e);
        }
        c.setNotificationUri(getContext().getContentResolver(),
                DocumentsContract.buildChildDocumentsUri(authority(getContext()), parentId));
        return c;
    }

    @Override
    public String getDocumentType(String documentId) throws FileNotFoundException {
        return mime(stat(documentId));
    }

    @Override
    public boolean isChildDocument(String parentId, String documentId) {
        return documentId.startsWith(parentId.endsWith("/") ? parentId : parentId + "/");
    }

    private JSONObject stat(String id) throws FileNotFoundException {
        try {
            return new JSONObject(drive().diskStat(check(id)));
        } catch (FileNotFoundException e) {
            throw e;
        } catch (Exception e) {
            throw notFound(e);
        }
    }

    private static String check(String id) throws FileNotFoundException {
        if (id == null || !(id.equals(ROOT) || id.startsWith(ROOT + "/")) || id.contains("/../") || id.endsWith("/..")) {
            throw new FileNotFoundException(id);
        }
        return id;
    }

    private static FileNotFoundException notFound(Exception e) {
        FileNotFoundException f = new FileNotFoundException(String.valueOf(e.getMessage()));
        f.initCause(e);
        return f;
    }

    static String mime(JSONObject i) {
        if (i.optBoolean("dir")) return Document.MIME_TYPE_DIR;
        String m = i.optString("mime");
        if (!m.isEmpty() && !"application/octet-stream".equals(m)) return m;
        String name = i.optString("name");
        int dot = name.lastIndexOf('.');
        if (dot >= 0) {
            String g = MimeTypeMap.getSingleton().getMimeTypeFromExtension(name.substring(dot + 1).toLowerCase(Locale.ROOT));
            if (g != null) return g;
        }
        return "application/octet-stream";
    }

    private static long millis(String iso) {
        if (iso == null || iso.isEmpty() || iso.startsWith("0001")) return 0;
        try {
            SimpleDateFormat f = new SimpleDateFormat("yyyy-MM-dd'T'HH:mm:ss", Locale.ROOT);
            f.setTimeZone(TimeZone.getTimeZone("UTC"));
            Date d = f.parse(iso.length() >= 19 ? iso.substring(0, 19) : iso);
            return d != null ? d.getTime() : 0;
        } catch (Exception e) {
            return 0;
        }
    }

    private static void addRow(MatrixCursor c, JSONObject i) {
        boolean dir = i.optBoolean("dir");
        String path = i.optString("path");
        int flags = Document.FLAG_SUPPORTS_DELETE | Document.FLAG_SUPPORTS_RENAME | Document.FLAG_SUPPORTS_MOVE;
        flags |= dir ? Document.FLAG_DIR_SUPPORTS_CREATE : Document.FLAG_SUPPORTS_WRITE;
        if (ROOT.equals(path)) flags = Document.FLAG_DIR_SUPPORTS_CREATE;
        MatrixCursor.RowBuilder r = c.newRow();
        r.add(Document.COLUMN_DOCUMENT_ID, path);
        r.add(Document.COLUMN_DISPLAY_NAME, ROOT.equals(path) ? "orOS" : i.optString("name"));
        r.add(Document.COLUMN_MIME_TYPE, mime(i));
        r.add(Document.COLUMN_SIZE, dir ? null : i.optLong("size"));
        long m = millis(i.optString("mtime"));
        r.add(Document.COLUMN_LAST_MODIFIED, m > 0 ? m : null);
        r.add(Document.COLUMN_FLAGS, flags);
    }

    private void changed(String path) {
        int slash = path.lastIndexOf('/');
        String parent = slash > 0 ? path.substring(0, slash) : ROOT;
        getContext().getContentResolver().notifyChange(
                DocumentsContract.buildChildDocumentsUri(authority(getContext()), parent), null);
    }

    // ---------- reading and writing ----------

    @Override
    public ParcelFileDescriptor openDocument(String documentId, String mode, CancellationSignal signal) throws FileNotFoundException {
        String id = check(documentId);
        Drive d = drive();
        if ("r".equals(mode)) {
            try {
                return ParcelFileDescriptor.open(new File(d.diskFetch(id)), ParcelFileDescriptor.MODE_READ_ONLY);
            } catch (FileNotFoundException e) {
                throw e;
            } catch (Exception e) {
                throw notFound(e);
            }
        }
        // A write goes to a scratch copy, uploaded when the app closes it.
        try {
            JSONObject st = stat(id);
            if (st.optBoolean("dir")) throw new FileNotFoundException(id);
            String base = st.optString("hash");
            File tmp = new File(d.diskTempPath());
            boolean keep = mode.contains("r") || mode.contains("a"); // "rw", "wa": start from the content
            if (keep) copy(new File(d.diskFetch(id)), tmp);
            int pfdMode = ParcelFileDescriptor.parseMode(mode);
            return ParcelFileDescriptor.open(tmp, pfdMode, closer, e -> {
                try {
                    if (e == null) {
                        d.diskPut(id, tmp.getAbsolutePath(), base);
                        changed(id);
                    } else {
                        Log.w(TAG, "write of " + id + " not finished: " + e);
                        notSaved(id, String.valueOf(e.getMessage()));
                    }
                } catch (Exception ex) {
                    Log.w(TAG, "upload of " + id + " failed: " + ex);
                    notSaved(id, String.valueOf(ex.getMessage()));
                } finally {
                    //noinspection ResultOfMethodCallIgnored
                    tmp.delete();
                }
            });
        } catch (FileNotFoundException e) {
            throw e;
        } catch (Exception e) {
            throw notFound(e);
        }
    }

    /** A save that did not reach orOS: the app that wrote it thinks it
     *  is saved, so tell the person (if notifications are allowed). */
    private void notSaved(String id, String why) {
        Context c = getContext();
        String text = c.getString(R.string.drive_not_saved_text, id.substring(id.lastIndexOf('/') + 1), why);
        Notify.message(c, id, 2, c.getString(R.string.drive_not_saved_title), text, Notification.CATEGORY_ERROR);
    }

    private static void copy(File from, File to) throws Exception {
        try (InputStream in = new FileInputStream(from); OutputStream out = new FileOutputStream(to)) {
            byte[] buf = new byte[64 * 1024];
            int n;
            while ((n = in.read(buf)) > 0) out.write(buf, 0, n);
        }
    }

    @Override
    public String createDocument(String parentId, String mimeType, String displayName) throws FileNotFoundException {
        String parent = check(parentId);
        Drive d = drive();
        String name = displayName.replace('/', '_').trim();
        if (name.isEmpty()) name = "file";
        boolean dir = Document.MIME_TYPE_DIR.equals(mimeType);
        if (!dir && name.indexOf('.') < 0 && mimeType != null) {
            String ext = MimeTypeMap.getSingleton().getExtensionFromMimeType(mimeType);
            if (ext != null) name = name + "." + ext;
        }
        String id = free(d, parent, name);
        try {
            if (dir) {
                d.diskMkdir(id);
            } else {
                File tmp = new File(d.diskTempPath());
                try {
                    d.diskPut(id, tmp.getAbsolutePath(), "");
                } finally {
                    //noinspection ResultOfMethodCallIgnored
                    tmp.delete();
                }
            }
        } catch (Exception e) {
            throw notFound(e);
        }
        changed(id);
        return id;
    }

    /** parent/name, or "name (1).ext"… when taken. */
    private static String free(Drive d, String parent, String name) {
        String base = name, ext = "";
        int dot = name.lastIndexOf('.');
        if (dot > 0) { base = name.substring(0, dot); ext = name.substring(dot); }
        String id = parent + "/" + name;
        for (int n = 1; n < 1000; n++) {
            try {
                d.diskStat(id);
            } catch (Exception e) {
                return id; // not found: free
            }
            id = parent + "/" + base + " (" + n + ")" + ext;
        }
        return id;
    }

    @Override
    public void deleteDocument(String documentId) throws FileNotFoundException {
        String id = check(documentId);
        try {
            drive().diskRemove(id);
        } catch (FileNotFoundException e) {
            throw e;
        } catch (Exception e) {
            throw notFound(e);
        }
        changed(id);
    }

    @Override
    public String renameDocument(String documentId, String displayName) throws FileNotFoundException {
        String id = check(documentId);
        int slash = id.lastIndexOf('/');
        String to = id.substring(0, slash) + "/" + displayName.replace('/', '_').trim();
        try {
            drive().diskRename(id, to);
        } catch (FileNotFoundException e) {
            throw e;
        } catch (Exception e) {
            throw notFound(e);
        }
        changed(id);
        changed(to);
        return to;
    }

    @Override
    public String moveDocument(String sourceDocumentId, String sourceParentDocumentId, String targetParentDocumentId)
            throws FileNotFoundException {
        String id = check(sourceDocumentId);
        String to = check(targetParentDocumentId) + id.substring(id.lastIndexOf('/'));
        try {
            drive().diskRename(id, to);
        } catch (FileNotFoundException e) {
            throw e;
        } catch (Exception e) {
            throw notFound(e);
        }
        changed(id);
        changed(to);
        return to;
    }
}
