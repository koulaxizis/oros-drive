package online.useoros.drive.app;

import android.content.ContentResolver;
import android.content.Context;
import android.content.SharedPreferences;
import android.database.Cursor;
import android.net.Uri;
import android.os.Build;
import android.os.ParcelFileDescriptor;
import android.provider.DocumentsContract;
import android.provider.DocumentsContract.Document;
import android.security.keystore.KeyGenParameterSpec;
import android.security.keystore.KeyProperties;
import android.util.Base64;

import org.json.JSONArray;
import org.json.JSONException;
import org.json.JSONObject;

import java.io.File;
import java.io.FileInputStream;
import java.io.FileOutputStream;
import java.io.IOException;
import java.io.InputStream;
import java.io.OutputStream;
import java.nio.ByteBuffer;
import java.nio.channels.FileChannel;
import java.nio.charset.StandardCharsets;
import java.security.KeyStore;
import java.text.SimpleDateFormat;
import java.util.Date;
import java.util.Locale;

import javax.crypto.Cipher;
import javax.crypto.KeyGenerator;
import javax.crypto.SecretKey;
import javax.crypto.spec.GCMParameterSpec;

import online.useoros.drive.mobile.Drive;
import online.useoros.drive.mobile.Folders;
import online.useoros.drive.mobile.Secrets;

/**
 * orOS Drive on the phone (DRIVE-A): the Go core from koulaxizis/oros-drive
 * (mobile/, bound into orosdrive.aar) mirrors folders the person picked
 * with the system folder picker into the encrypted orOS Vault. This
 * class gives the core what only Android has: the picked folders
 * (Storage Access Framework) and the Android Keystore.
 */
public final class DriveCore {
    private static Drive drive;

    /** The process-wide core, created on first use. */
    public static synchronized Drive get(Context c) throws Exception {
        if (drive == null) {
            Context app = c.getApplicationContext();
            File dir = new File(app.getFilesDir(), "drive");
            //noinspection ResultOfMethodCallIgnored
            dir.mkdirs();
            String device = Build.MODEL != null && !Build.MODEL.isEmpty() ? Build.MODEL : "Phone";
            drive = new Drive(dir.getAbsolutePath(), device, new SafFolders(app), new KeystoreSecrets(app));
        }
        return drive;
    }

    /** False until the person has signed in on this phone once: the
     *  secrets store is empty. Cheap, so a file picker asking for roots
     *  does not load the Go core for people who never set up Drive. */
    static boolean everSignedIn(Context c) {
        return !c.getApplicationContext().getSharedPreferences(KeystoreSecrets.PREFS, Context.MODE_PRIVATE).getAll().isEmpty();
    }

    private DriveCore() {}

    // ---------- Folders over the Storage Access Framework ----------

    /** A folder is its tree URI (the grant from ACTION_OPEN_DOCUMENT_TREE);
     *  paths inside it are "a/b.txt". A missing entry throws "not-found". */
    static final class SafFolders implements Folders {
        private final Context app;
        private final ContentResolver cr;

        SafFolders(Context app) { this.app = app; this.cr = app.getContentResolver(); }

        private static final String[] COLS = {
            Document.COLUMN_DOCUMENT_ID, Document.COLUMN_DISPLAY_NAME, Document.COLUMN_MIME_TYPE,
            Document.COLUMN_SIZE, Document.COLUMN_LAST_MODIFIED, Document.COLUMN_FLAGS
        };

        private static final class Doc {
            String id, name, mime;
            long size, modified;
            boolean dir() { return Document.MIME_TYPE_DIR.equals(mime); }
        }

        private static Doc row(Cursor c) {
            Doc d = new Doc();
            d.id = c.getString(0);
            d.name = c.getString(1);
            d.mime = c.getString(2);
            d.size = c.isNull(3) ? 0 : c.getLong(3);
            d.modified = c.isNull(4) ? 0 : c.getLong(4);
            return d;
        }

        private Doc query(Uri tree, String docId) throws IOException {
            Uri u = DocumentsContract.buildDocumentUriUsingTree(tree, docId);
            try (Cursor c = cr.query(u, COLS, null, null, null)) {
                if (c == null || !c.moveToFirst()) throw new IOException("not-found");
                return row(c);
            }
        }

        private java.util.List<Doc> children(Uri tree, String docId) throws IOException {
            Uri u = DocumentsContract.buildChildDocumentsUriUsingTree(tree, docId);
            java.util.List<Doc> out = new java.util.ArrayList<>();
            try (Cursor c = cr.query(u, COLS, null, null, null)) {
                if (c == null) throw new IOException("cannot list");
                while (c.moveToNext()) out.add(row(c));
            }
            return out;
        }

        private Doc child(Uri tree, String parentId, String name) throws IOException {
            for (Doc d : children(tree, parentId)) if (name.equals(d.name)) return d;
            return null;
        }

        /** Document ids of paths seen lately (a pass asks for the same
         *  paths several times); checked again on every use. */
        private final java.util.Map<String, String> ids = java.util.Collections.synchronizedMap(
                new java.util.LinkedHashMap<String, String>(256, 0.75f, true) {
                    @Override protected boolean removeEldestEntry(java.util.Map.Entry<String, String> e) { return size() > 4096; }
                });

        /** The document at rel, or throws "not-found". */
        private Doc resolve(Uri tree, String rel) throws IOException {
            String k = tree + "\u0000" + rel;
            String cached = ids.get(k);
            if (cached != null) {
                try {
                    Doc d = query(tree, cached);
                    String want = rel.isEmpty() ? null : rel.substring(rel.lastIndexOf('/') + 1);
                    if (want == null || want.equals(d.name)) return d;
                } catch (Exception ignored) { }
                ids.remove(k);
            }
            Doc d = walk(tree, rel);
            ids.put(k, d.id);
            return d;
        }

        private Doc walk(Uri tree, String rel) throws IOException {
            Doc d = query(tree, DocumentsContract.getTreeDocumentId(tree));
            if (rel.isEmpty()) return d;
            for (String seg : rel.split("/")) {
                if (!d.dir()) throw new IOException("not-found");
                Doc next = child(tree, d.id, seg);
                if (next == null) throw new IOException("not-found");
                d = next;
            }
            return d;
        }

        private static JSONObject info(Doc d) throws JSONException {
            JSONObject o = new JSONObject();
            o.put("name", d.name);
            o.put("dir", d.dir());
            o.put("regular", !d.dir());
            o.put("size", d.size);
            o.put("mtime", d.modified * 1_000_000L);   // ms → ns
            return o;
        }

        @Override public String stat(String folder, String rel) throws Exception {
            return info(resolve(Uri.parse(folder), rel)).toString();
        }

        @Override public String list(String folder, String rel) throws Exception {
            Uri tree = Uri.parse(folder);
            Doc d = resolve(tree, rel);
            if (!d.dir()) throw new IOException("not a folder");
            JSONArray a = new JSONArray();
            for (Doc c : children(tree, d.id)) a.put(info(c));
            return a.toString();
        }

        @Override public byte[] read(String folder, String rel, long offset, long n) throws Exception {
            Uri tree = Uri.parse(folder);
            Doc d = resolve(tree, rel);
            Uri u = DocumentsContract.buildDocumentUriUsingTree(tree, d.id);
            try (ParcelFileDescriptor pfd = cr.openFileDescriptor(u, "r")) {
                if (pfd == null) throw new IOException("cannot open");
                try (FileInputStream in = new FileInputStream(pfd.getFileDescriptor())) {
                    FileChannel ch = in.getChannel();
                    ByteBuffer buf = ByteBuffer.allocate((int) Math.min(n, Math.max(0, d.size - offset) + 1));
                    ch.position(offset);
                    while (buf.hasRemaining()) {
                        if (ch.read(buf) < 0) break;
                    }
                    byte[] out = new byte[buf.position()];
                    buf.flip();
                    buf.get(out);
                    return out;
                }
            }
        }

        /** Folders on the way, created as needed. */
        private Doc ensureDir(Uri tree, String rel) throws IOException {
            Doc d = query(tree, DocumentsContract.getTreeDocumentId(tree));
            if (rel.isEmpty()) return d;
            for (String seg : rel.split("/")) {
                Doc next = child(tree, d.id, seg);
                if (next == null) {
                    Uri made = DocumentsContract.createDocument(cr,
                            DocumentsContract.buildDocumentUriUsingTree(tree, d.id), Document.MIME_TYPE_DIR, seg);
                    if (made == null) throw new IOException("cannot create folder " + seg);
                    next = query(tree, DocumentsContract.getDocumentId(made));
                } else if (!next.dir()) {
                    throw new IOException(seg + " is a file");
                }
                d = next;
            }
            return d;
        }

        /** Writes into a temporary document and renames it over the old
         *  one where the provider can rename (most can); otherwise the
         *  old document is rewritten in place. */
        @Override public String write(String folder, String rel, byte[] data) throws Exception {
            Uri tree = Uri.parse(folder);
            int slash = rel.lastIndexOf('/');
            String parentRel = slash < 0 ? "" : rel.substring(0, slash);
            String name = rel.substring(slash + 1);
            Doc parent = ensureDir(tree, parentRel);
            Uri parentUri = DocumentsContract.buildDocumentUriUsingTree(tree, parent.id);
            Doc old = child(tree, parent.id, name);
            String tmpName = ".orosdrive-" + Long.toHexString(System.nanoTime());
            Uri tmp = DocumentsContract.createDocument(cr, parentUri, "application/octet-stream", tmpName);
            if (tmp != null) {
                try {
                    writeTo(tmp, data);
                    if (old != null) DocumentsContract.deleteDocument(cr, DocumentsContract.buildDocumentUriUsingTree(tree, old.id));
                    Uri renamed = DocumentsContract.renameDocument(cr, tmp, name);
                    Uri done = renamed != null ? renamed : tmp;
                    Doc d = query(tree, DocumentsContract.getDocumentId(done));
                    if (name.equals(d.name)) return info(d).toString();
                    // The provider changed the name (an added extension or
                    // " (1)"): fall back to writing in place below.
                    DocumentsContract.deleteDocument(cr, done);
                    old = null;
                } catch (Exception e) {
                    // No rename here: drop the temporary copy and write in place.
                    try { DocumentsContract.deleteDocument(cr, tmp); } catch (Exception ignored) { }
                    old = child(tree, parent.id, name);
                }
            }
            Uri target;
            if (old != null) {
                target = DocumentsContract.buildDocumentUriUsingTree(tree, old.id);
            } else {
                target = DocumentsContract.createDocument(cr, parentUri, "application/octet-stream", name);
                if (target == null) throw new IOException("cannot create " + name);
            }
            writeTo(target, data);
            return info(query(tree, DocumentsContract.getDocumentId(target))).toString();
        }

        private void writeTo(Uri u, byte[] data) throws IOException {
            try (OutputStream out = cr.openOutputStream(u, "wt")) {
                if (out == null) throw new IOException("cannot write");
                out.write(data);
            }
        }

        /** Copies the file into the app's own trash (kept 30 days), then
         *  deletes it, and removes folders the deletion left empty. */
        @Override public void remove(String folder, String rel) throws Exception {
            Uri tree = Uri.parse(folder);
            Doc d = resolve(tree, rel);
            Uri u = DocumentsContract.buildDocumentUriUsingTree(tree, d.id);
            File base = app.getExternalFilesDir(null);
            if (base == null) base = app.getFilesDir();
            String day = new SimpleDateFormat("yyyy-MM-dd", Locale.ROOT).format(new Date());
            File dst = new File(new File(new File(base, "Trash"), day), rel);
            //noinspection ResultOfMethodCallIgnored
            dst.getParentFile().mkdirs();
            try (InputStream in = cr.openInputStream(u); OutputStream out = new FileOutputStream(dst)) {
                if (in == null) throw new IOException("cannot read");
                byte[] buf = new byte[1 << 16];
                for (int n; (n = in.read(buf)) > 0; ) out.write(buf, 0, n);
            }
            DocumentsContract.deleteDocument(cr, u);
            String parent = rel.contains("/") ? rel.substring(0, rel.lastIndexOf('/')) : "";
            while (!parent.isEmpty()) {
                Doc p = resolve(tree, parent);
                if (!children(tree, p.id).isEmpty()) break;
                DocumentsContract.deleteDocument(cr, DocumentsContract.buildDocumentUriUsingTree(tree, p.id));
                parent = parent.contains("/") ? parent.substring(0, parent.lastIndexOf('/')) : "";
            }
            purgeTrash(new File(base, "Trash"));
        }

        private static void purgeTrash(File trash) {
            File[] days = trash.listFiles();
            if (days == null) return;
            long limit = System.currentTimeMillis() - 31L * 24 * 3600 * 1000;
            SimpleDateFormat f = new SimpleDateFormat("yyyy-MM-dd", Locale.ROOT);
            for (File day : days) {
                try {
                    Date d = f.parse(day.getName());
                    if (d != null && d.getTime() < limit) deleteTree(day);
                } catch (java.text.ParseException ignored) { }
            }
        }

        private static void deleteTree(File f) {
            File[] kids = f.listFiles();
            if (kids != null) for (File k : kids) deleteTree(k);
            //noinspection ResultOfMethodCallIgnored
            f.delete();
        }
    }

    // ---------- Secrets in the Android Keystore ----------

    /** Values sealed with an AES key that never leaves the Android
     *  Keystore, kept in private preferences. */
    static final class KeystoreSecrets implements Secrets {
        private static final String ALIAS = "oros-drive-secrets";
        static final String PREFS = "oros-drive-secrets";
        private final SharedPreferences prefs;

        KeystoreSecrets(Context app) { prefs = app.getSharedPreferences(PREFS, Context.MODE_PRIVATE); }

        private static SecretKey key() throws Exception {
            KeyStore ks = KeyStore.getInstance("AndroidKeyStore");
            ks.load(null);
            if (ks.containsAlias(ALIAS)) return ((KeyStore.SecretKeyEntry) ks.getEntry(ALIAS, null)).getSecretKey();
            KeyGenerator g = KeyGenerator.getInstance(KeyProperties.KEY_ALGORITHM_AES, "AndroidKeyStore");
            g.init(new KeyGenParameterSpec.Builder(ALIAS, KeyProperties.PURPOSE_ENCRYPT | KeyProperties.PURPOSE_DECRYPT)
                    .setBlockModes(KeyProperties.BLOCK_MODE_GCM)
                    .setEncryptionPaddings(KeyProperties.ENCRYPTION_PADDING_NONE)
                    .setKeySize(256)
                    .build());
            return g.generateKey();
        }

        @Override public String get(String name) throws Exception {
            String v = prefs.getString(name, null);
            if (v == null) return "";
            byte[] all = Base64.decode(v, Base64.NO_WRAP);
            Cipher c = Cipher.getInstance("AES/GCM/NoPadding");
            c.init(Cipher.DECRYPT_MODE, key(), new GCMParameterSpec(128, all, 0, 12));
            return new String(c.doFinal(all, 12, all.length - 12), StandardCharsets.UTF_8);
        }

        @Override public void set(String name, String value) throws Exception {
            Cipher c = Cipher.getInstance("AES/GCM/NoPadding");
            c.init(Cipher.ENCRYPT_MODE, key());
            byte[] iv = c.getIV();
            byte[] ct = c.doFinal(value.getBytes(StandardCharsets.UTF_8));
            byte[] all = new byte[iv.length + ct.length];
            System.arraycopy(iv, 0, all, 0, iv.length);
            System.arraycopy(ct, 0, all, iv.length, ct.length);
            prefs.edit().putString(name, Base64.encodeToString(all, Base64.NO_WRAP)).apply();
        }

        @Override public void delete(String name) { prefs.edit().remove(name).apply(); }
    }
}
