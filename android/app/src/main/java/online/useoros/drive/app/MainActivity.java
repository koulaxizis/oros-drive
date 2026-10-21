package online.useoros.drive.app;

import android.app.Activity;
import android.app.AlertDialog;
import android.content.Intent;
import android.net.Uri;
import android.os.Bundle;
import android.os.Handler;
import android.os.Looper;
import android.provider.DocumentsContract;
import android.text.InputType;
import android.text.format.DateUtils;
import android.text.format.Formatter;
import android.view.View;
import android.widget.EditText;
import android.widget.LinearLayout;
import android.widget.Switch;
import android.widget.TextView;
import android.widget.Toast;

import androidx.browser.customtabs.CustomTabsIntent;

import org.json.JSONArray;
import org.json.JSONObject;

import java.text.NumberFormat;
import java.text.SimpleDateFormat;
import java.util.Date;
import java.util.List;
import java.util.Locale;
import java.util.Set;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;

import online.useoros.drive.mobile.Drive;

/**
 * orOS Drive for Android (online.useoros.drive): sign in (Dropbox,
 * Nextcloud or any WebDAV folder), unlock, pick folders to mirror and
 * photo folders to back up, browse the orOS disk, see each folder's
 * state. Every call into the Go core runs off the main thread.
 */
public class MainActivity extends Activity {
    private static final int PICK = 41;
    private static final int MEDIA = 2;
    private final ExecutorService io = Executors.newSingleThreadExecutor();
    private final Handler main = new Handler(Looper.getMainLooper());
    private Ui ui;
    private Drive drive;
    private LinearLayout body;
    private JSONObject status = new JSONObject();
    private boolean askCreate;
    private String unlockMsg = "";
    private Boolean shown; // the Files app root was last told this
    private long cacheBytes;
    /** The phone's photo folders, null until looked up. */
    private List<CameraUploader.Folder> media;
    private final Runnable poll = new Runnable() {
        @Override public void run() { refresh(); main.postDelayed(this, 3000); }
    };

    @Override
    protected void onCreate(Bundle b) {
        super.onCreate(b);
        ui = new Ui(this, getString(R.string.drive_title), false);
        body = ui.body;
        // Upload progress, conflict copies and unsaved files are told
        // in notifications (Android 13+ asks once).
        if (android.os.Build.VERSION.SDK_INT >= 33 && b == null
                && checkSelfPermission(android.Manifest.permission.POST_NOTIFICATIONS) != android.content.pm.PackageManager.PERMISSION_GRANTED) {
            requestPermissions(new String[]{android.Manifest.permission.POST_NOTIFICATIONS}, 1);
        }
        io.execute(() -> {
            try {
                drive = DriveCore.get(this);
            } catch (Exception e) {
                main.post(() -> {
                    body.removeAllViews();
                    ui.card(body).addView(ui.text(getString(R.string.drive_unavailable, String.valueOf(e.getMessage())), 15));
                });
            }
        });
    }

    @Override protected void onResume() { super.onResume(); main.post(poll); findMedia(); }
    @Override protected void onPause() { super.onPause(); main.removeCallbacks(poll); }
    @Override protected void onDestroy() { super.onDestroy(); io.shutdown(); }

    /** Runs a core call off the main thread, then refreshes; an error
     *  shows as a toast. */
    private void call(CoreCall c) {
        if (drive == null) return;
        io.execute(() -> {
            String err = null;
            try { c.run(drive); } catch (Exception e) { err = e.getMessage(); }
            String shown = err;
            main.post(() -> {
                if (shown != null && !shown.isEmpty()) Toast.makeText(this, shown, Toast.LENGTH_LONG).show();
                refresh();
            });
        });
    }

    private interface CoreCall { void run(Drive d) throws Exception; }

    private void refresh() {
        if (drive == null) return;
        io.execute(() -> {
            try {
                JSONObject s = new JSONObject(drive.status());
                boolean ready = s.optBoolean("signedIn") && s.optBoolean("unlocked");
                long cache = ready ? drive.diskCacheSize() : 0;
                if (shown == null || shown != ready) {
                    shown = ready;
                    DriveProvider.rootsChanged(this);
                }
                main.post(() -> { status = s; cacheBytes = cache; render(); });
            } catch (Exception ignored) { }
        });
    }

    /** Looks up the photo folders (MediaStore) again, off the main thread. */
    private void findMedia() {
        if (!CameraUploader.allowed(this)) return;
        io.execute(() -> {
            List<CameraUploader.Folder> l = CameraUploader.discover(this);
            main.post(() -> { media = l; render(); });
        });
    }

    private void render() {
        // Keep a field being typed in: rebuild only when nothing has focus.
        View f = getCurrentFocus();
        if (f instanceof EditText) return;
        body.removeAllViews();
        JSONObject settings = status.optJSONObject("settings");
        String device = settings != null ? settings.optString("device") : "";

        if (!status.optBoolean("signedIn")) {
            body.addView(ui.dim(getString(R.string.drive_intro, device)));
            renderLogin();
            return;
        }
        if (!status.optBoolean("unlocked")) {
            body.addView(ui.dim(getString(R.string.drive_signed_in, status.optString("account"))));
            renderUnlock();
            return;
        }
        renderSync();
        renderMedia(device);
        renderFolders(settings, device);
        renderFiles();
        renderOptions(settings);
        renderAccount();
        renderEvents();
    }

    private String loginMsg = "";
    private EditText davUrl, davDir, davUser, davPass;

    /** Dropbox, Nextcloud (Login Flow v2) or a WebDAV folder with an
     *  app password. The server is checked before anything is saved. */
    private void renderLogin() {
        LinearLayout c = ui.card(body);
        c.addView(ui.heading("Dropbox"));
        c.addView(ui.dim(getString(R.string.drive_login_text)));
        c.addView(ui.row(ui.primary(getString(R.string.drive_login), v -> login())));

        c = ui.card(body);
        c.addView(ui.heading(getString(R.string.drive_or_server)));
        c.addView(ui.dim(getString(R.string.drive_server_text)));
        davUrl = field(c, davUrl, R.string.drive_server_url, InputType.TYPE_CLASS_TEXT | InputType.TYPE_TEXT_VARIATION_URI);
        davDir = field(c, davDir, R.string.drive_server_dir, InputType.TYPE_CLASS_TEXT);
        c.addView(ui.row(ui.primary(getString(R.string.drive_nextcloud), v -> nextcloud())));
        TextView other = ui.heading(getString(R.string.drive_other_server));
        other.setPadding(0, ui.dp(16), 0, ui.dp(4));
        c.addView(other);
        davUser = field(c, davUser, R.string.drive_server_user, InputType.TYPE_CLASS_TEXT);
        davPass = field(c, davPass, R.string.drive_server_pass, InputType.TYPE_CLASS_TEXT | InputType.TYPE_TEXT_VARIATION_PASSWORD);
        c.addView(ui.row(ui.secondary(getString(R.string.drive_connect), v -> webdav())));

        String login = status.optString("login");
        String m = loginMsg;
        if (m.isEmpty() && "waiting".equals(login)) m = getString(R.string.drive_login_wait);
        else if (m.isEmpty() && !login.isEmpty()) m = davText(login);
        if (!m.isEmpty()) ui.card(body).addView(ui.text(m, 14));
    }

    /** Leaves the fields (and hides the keyboard), so the screen can
     *  re-render with the result. */
    private void unfocus() {
        View f = getCurrentFocus();
        if (f != null) {
            android.view.inputmethod.InputMethodManager im = getSystemService(android.view.inputmethod.InputMethodManager.class);
            if (im != null) im.hideSoftInputFromWindow(f.getWindowToken(), 0);
        }
        body.requestFocus();
    }

    /** A field that keeps what was typed across re-renders. */
    private EditText field(LinearLayout into, EditText old, int hint, int type) {
        EditText e = ui.field(getString(hint), type);
        if (old != null) e.setText(old.getText());
        into.addView(e);
        return e;
    }

    /** A "dav-…" code from the core as one sentence. */
    private String davText(String code) {
        if (code == null) return "";
        int id = getResources().getIdentifier(code.replace('-', '_'), "string", getPackageName());
        return code.startsWith("dav-") && id != 0 ? getString(id) : code;
    }

    private void nextcloud() {
        String url = davUrl.getText().toString().trim(), dir = davDir.getText().toString().trim();
        if (drive == null || url.isEmpty()) return;
        loginMsg = getString(R.string.drive_checking);
        unfocus();
        render();
        io.execute(() -> {
            try {
                String page = drive.nextcloudLoginURL(url, dir);
                main.post(() -> {
                    loginMsg = "";
                    if (page.startsWith("https://") || page.startsWith("http://")) openPage(page);
                    refresh();
                });
            } catch (Exception e) {
                main.post(() -> { loginMsg = davText(e.getMessage()); render(); });
            }
        });
    }

    private void webdav() {
        String url = davUrl.getText().toString().trim(), dir = davDir.getText().toString().trim();
        String user = davUser.getText().toString().trim(), pass = davPass.getText().toString();
        if (drive == null) return;
        loginMsg = getString(R.string.drive_checking);
        unfocus();
        render();
        io.execute(() -> {
            String msg = "";
            try {
                drive.connectWebDAV(url, user, pass, dir);
            } catch (Exception e) {
                msg = davText(e.getMessage());
            }
            String shown = msg;
            main.post(() -> {
                loginMsg = shown;
                if (shown.isEmpty() && davPass != null) davPass.setText("");
                refresh();
            });
        });
    }

    private void openPage(String url) {
        try {
            new CustomTabsIntent.Builder().build().launchUrl(this, Uri.parse(url));
        } catch (Exception e) {
            startActivity(new Intent(Intent.ACTION_VIEW, Uri.parse(url)));
        }
    }

    private void login() {
        if (drive == null) return;
        io.execute(() -> {
            try {
                String url = drive.loginURL();
                main.post(() -> { openPage(url); refresh(); });
            } catch (Exception e) {
                main.post(() -> Toast.makeText(this, e.getMessage(), Toast.LENGTH_LONG).show());
            }
        });
    }

    private void renderUnlock() {
        LinearLayout c = ui.card(body);
        c.addView(ui.heading(getString(R.string.drive_unlock)));
        c.addView(ui.dim(getString("reset".equals(status.optString("needsKey"))
                ? R.string.drive_unlock_reset : R.string.drive_unlock_text)));
        EditText pass = ui.field(getString(R.string.drive_passphrase), InputType.TYPE_CLASS_TEXT | InputType.TYPE_TEXT_VARIATION_PASSWORD);
        c.addView(pass);
        c.addView(ui.row(ui.primary(getString(R.string.drive_unlock), v -> unlock(pass, false))));
        if (askCreate) {
            c.addView(ui.text(getString(R.string.drive_no_vault), 14));
            c.addView(ui.row(ui.secondary(getString(R.string.drive_create), v -> unlock(pass, true))));
        }
        if (!unlockMsg.isEmpty()) {
            TextView m = ui.text(unlockMsg, 14);
            m.setTextColor(ui.danger);
            c.addView(m);
        }
        c = ui.card(body);
        c.addView(ui.row(ui.danger(getString(R.string.drive_logout), v -> logout())));
    }

    private void unlock(EditText pass, boolean create) {
        String p = pass.getText().toString();
        pass.clearFocus();
        if (drive == null || p.isEmpty()) return;
        io.execute(() -> {
            String msg = "";
            boolean noVault = false;
            try {
                drive.unlock(p, create);
                DriveWorker.schedule(this);
            } catch (Exception e) {
                String m = String.valueOf(e.getMessage());
                if (m.contains("no-vault")) noVault = true;
                else msg = m.contains("locked") ? getString(R.string.drive_wrong_pass) : m;
            }
            boolean nv = noVault;
            String shown = msg;
            main.post(() -> { askCreate = nv; unlockMsg = shown; refresh(); });
        });
    }

    /** The state at a glance, Sync now / Pause, and the disk. */
    private void renderSync() {
        LinearLayout c = ui.card(body);
        boolean paused = status.optBoolean("paused");
        String err = status.optString("error");
        String head;
        if (status.optBoolean("syncing")) head = getString(R.string.drive_syncing);
        else if (paused) head = getString(R.string.drive_paused);
        else if (!err.isEmpty()) head = getString(R.string.drive_error, err);
        else head = getString(R.string.drive_ok);
        TextView h = ui.heading(head);
        h.setTextSize(18);
        if (!err.isEmpty() && !paused) h.setTextColor(ui.danger);
        c.addView(h);
        c.addView(ui.dim(getString(R.string.drive_last, when(status.optString("lastSync")))));
        c.addView(ui.row(
                ui.primary(getString(R.string.drive_sync_now), v -> call(Drive::sync)),
                ui.secondary(getString(paused ? R.string.drive_resume : R.string.drive_pause), v -> call(d -> d.setPaused(!paused)))));
        LinearLayout b = ui.card(body);
        b.setPadding(ui.dp(4), ui.dp(4), ui.dp(4), ui.dp(4));
        b.addView(ui.item("🗂", getString(R.string.drive_browse), getString(R.string.drive_browse_sub),
                v -> startActivity(new Intent(this, BrowseActivity.class))));
    }

    private String when(String iso) {
        if (iso.isEmpty() || iso.startsWith("0001-")) return "–";
        try {
            SimpleDateFormat f = new SimpleDateFormat("yyyy-MM-dd'T'HH:mm:ss", Locale.ROOT);
            f.setTimeZone(java.util.TimeZone.getTimeZone("UTC"));
            Date d = f.parse(iso.substring(0, 19));
            if (d == null) return iso;
            return DateUtils.getRelativeTimeSpanString(d.getTime(), System.currentTimeMillis(), DateUtils.MINUTE_IN_MILLIS).toString();
        } catch (Exception e) {
            return iso;
        }
    }

    /** Photo and video backup: the phone's media folders, each on or off. */
    private void renderMedia(String device) {
        LinearLayout c = ui.card(body);
        c.addView(ui.heading(getString(R.string.drive_media_title)));
        c.addView(ui.dim(getString(R.string.drive_media_text, device)));
        if (!CameraUploader.allowed(this)) {
            c.addView(ui.row(ui.primary(getString(R.string.drive_media_find), v -> requestPermissions(CameraUploader.permissions(), MEDIA))));
            return;
        }
        if (media == null) {
            c.addView(ui.dim(getString(R.string.drive_media_looking)));
            return;
        }
        if (media.isEmpty()) {
            c.addView(ui.dim(getString(R.string.drive_media_none)));
            return;
        }
        Set<String> on = CameraUploader.chosen(this);
        NumberFormat nf = NumberFormat.getIntegerInstance();
        for (CameraUploader.Folder f : media) {
            boolean chosen = on.contains(f.key);
            String name = CameraUploader.CAMERA.equals(f.key) ? getString(R.string.drive_media_camera) : f.name;
            Switch s = ui.toggle(name, chosen);
            s.setOnCheckedChangeListener((b, yes) -> {
                if (!yes) { CameraUploader.choose(this, f.key, false, false); findMedia(); return; }
                new AlertDialog.Builder(this)
                        .setTitle(name)
                        .setMessage(getResources().getQuantityString(R.plurals.drive_media_ask, f.count, nf.format(f.count)))
                        .setPositiveButton(R.string.drive_media_all, (d, w) -> mediaOn(f.key, true))
                        .setNeutralButton(R.string.drive_media_new, (d, w) -> mediaOn(f.key, false))
                        .setNegativeButton(R.string.drive_cancel, null)
                        .setOnDismissListener(d -> render())
                        .show();
            });
            c.addView(s);
            String sub = getResources().getQuantityString(R.plurals.drive_media_count, f.count, nf.format(f.count))
                    + " · " + Formatter.formatShortFileSize(this, f.bytes);
            if (chosen) {
                sub += " · " + (f.waiting > 0 ? getString(R.string.drive_media_waiting, nf.format(f.waiting))
                        : getString(R.string.drive_media_sent));
            }
            TextView t = ui.dim(sub);
            t.setPadding(0, 0, 0, ui.dp(chosen && !CameraUploader.all(this, f.key) ? 0 : 6));
            c.addView(t);
            if (chosen && !CameraUploader.all(this, f.key)) {
                c.addView(ui.link(getString(R.string.drive_media_older), v -> {
                    CameraUploader.older(this, f.key);
                    DriveWorker.now(this);
                    findMedia();
                }));
            }
        }
    }

    private void mediaOn(String key, boolean older) {
        CameraUploader.choose(this, key, true, older);
        DriveWorker.schedule(this);
        DriveWorker.now(this);
        findMedia();
    }

    private void renderFolders(JSONObject settings, String device) {
        LinearLayout c = ui.card(body);
        c.addView(ui.heading(getString(R.string.drive_folders)));
        JSONArray folders = settings != null ? settings.optJSONArray("folders") : null;
        JSONObject states = status.optJSONObject("folders");
        if (folders == null || folders.length() == 0) c.addView(ui.dim(getString(R.string.drive_intro, device)));
        for (int i = 0; folders != null && i < folders.length(); i++) {
            JSONObject p = folders.optJSONObject(i);
            if (p == null) continue;
            String id = p.optString("id");
            JSONObject f = states != null ? states.optJSONObject(id) : null;
            if (f == null) f = new JSONObject();
            String remote = p.optString("remote");
            TextView name = ui.text(remote.substring(remote.lastIndexOf('/') + 1), 15);
            name.setTypeface(android.graphics.Typeface.create("sans-serif-medium", android.graphics.Typeface.NORMAL));
            name.setPadding(0, ui.dp(i == 0 ? 4 : 14), 0, 0);
            c.addView(name);
            c.addView(ui.dim(folderLabel(p.optString("local")) + "  →  orOS › " + remote.replaceFirst("^/internal/", "").replace("/", " › ")));
            boolean paused = p.optBoolean("paused");
            String state = f.optString("state");
            String line;
            if (paused) line = getString(R.string.drive_paused);
            else if ("missing".equals(state)) line = getString(R.string.drive_missing);
            else if ("reset".equals(state)) line = getString(R.string.drive_reset);
            else if ("error".equals(state)) line = getString(R.string.drive_error, f.optString("error"));
            else if ("held".equals(state)) line = getString(R.string.drive_held, f.optInt("heldDeletes"));
            else line = getString(R.string.drive_ok) + (f.has("files") ? " · " + getString(R.string.drive_files, f.optInt("files")) : "");
            JSONArray lo = f.optJSONArray("localOnly"), sk = f.optJSONArray("skipped");
            if (lo != null && lo.length() > 0) line += "\n" + getString(R.string.drive_local_only, lo.length());
            if (sk != null && sk.length() > 0) line += "\n" + getString(R.string.drive_skipped, sk.length());
            c.addView(ui.text(line, 14));
            if ("held".equals(state) && !paused) {
                c.addView(ui.row(
                        ui.primary(getString(R.string.drive_approve), v -> call(d -> { d.approveDeletes(id); d.sync(); })),
                        ui.secondary(getString(R.string.drive_hold), v -> call(d -> d.pauseFolder(id, true)))));
            } else {
                c.addView(ui.row(
                        ui.secondary(getString(paused ? R.string.drive_resume : R.string.drive_pause), v -> call(d -> d.pauseFolder(id, !paused))),
                        ui.secondary(getString(R.string.drive_remove), v -> new AlertDialog.Builder(this)
                                .setMessage(R.string.drive_remove_ask)
                                .setPositiveButton(R.string.drive_remove, (dlg, w) -> {
                                    String grant = p.optString("local");
                                    call(d -> d.removeFolder(id));
                                    try {
                                        getContentResolver().releasePersistableUriPermission(Uri.parse(grant),
                                                Intent.FLAG_GRANT_READ_URI_PERMISSION | Intent.FLAG_GRANT_WRITE_URI_PERMISSION);
                                    } catch (SecurityException ignored) { }
                                })
                                .setNegativeButton(R.string.drive_cancel, null)
                                .show())));
            }
        }
        LinearLayout add = ui.row(ui.primary(getString(R.string.drive_add), v -> {
            Intent i = new Intent(Intent.ACTION_OPEN_DOCUMENT_TREE);
            i.addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION | Intent.FLAG_GRANT_WRITE_URI_PERMISSION
                    | Intent.FLAG_GRANT_PERSISTABLE_URI_PERMISSION);
            startActivityForResult(i, PICK);
        }));
        add.setPadding(0, ui.dp(12), 0, 0);
        c.addView(add);
    }

    /** "primary:Documents/Work" → "Documents/Work". */
    private static String folderLabel(String grant) {
        try {
            String id = DocumentsContract.getTreeDocumentId(Uri.parse(grant));
            return id.contains(":") ? id.substring(id.indexOf(':') + 1) : id;
        } catch (Exception e) {
            return grant;
        }
    }

    @Override
    protected void onActivityResult(int req, int res, Intent data) {
        super.onActivityResult(req, res, data);
        if (req != PICK || res != RESULT_OK || data == null || data.getData() == null) return;
        Uri tree = data.getData();
        try {
            getContentResolver().takePersistableUriPermission(tree,
                    Intent.FLAG_GRANT_READ_URI_PERMISSION | Intent.FLAG_GRANT_WRITE_URI_PERMISSION);
        } catch (SecurityException e) {
            Toast.makeText(this, e.getMessage(), Toast.LENGTH_LONG).show();
            return;
        }
        String label = folderLabel(tree.toString());
        EditText name = ui.field(getString(R.string.drive_name_title), InputType.TYPE_CLASS_TEXT);
        name.setText(label.contains("/") ? label.substring(label.lastIndexOf('/') + 1) : label);
        LinearLayout box = new LinearLayout(this);
        box.setPadding(ui.dp(20), ui.dp(8), ui.dp(20), 0);
        box.addView(name);
        new AlertDialog.Builder(this)
                .setTitle(R.string.drive_name_title)
                .setView(box)
                .setPositiveButton(R.string.drive_add, (d, w) -> {
                    String n = name.getText().toString().trim();
                    call(dr -> {
                        dr.addFolder(tree.toString(), n);
                        main.post(() -> Toast.makeText(this, R.string.drive_added, Toast.LENGTH_LONG).show());
                        DriveWorker.schedule(this);
                        DriveWorker.now(this);
                    });
                })
                .setNegativeButton(R.string.drive_cancel, null)
                .show();
    }

    /** The disk in the Files app (DriveProvider) and its downloaded copies. */
    private void renderFiles() {
        LinearLayout c = ui.card(body);
        c.addView(ui.heading(getString(R.string.drive_files_title)));
        c.addView(ui.dim(getString(R.string.drive_files_text)));
        c.addView(ui.text(getString(R.string.drive_cache, Formatter.formatShortFileSize(this, cacheBytes)), 14));
        if (cacheBytes > 0) c.addView(ui.row(ui.secondary(getString(R.string.drive_clear_cache), v -> call(Drive::diskClearCache))));
    }

    private void renderOptions(JSONObject settings) {
        LinearLayout c = ui.card(body);
        c.addView(ui.heading(getString(R.string.drive_options)));
        android.content.SharedPreferences prefs = getSharedPreferences(DriveWorker.PREFS, MODE_PRIVATE);
        Switch wifi = ui.toggle(getString(R.string.drive_wifi), prefs.getBoolean("wifi-only", true));
        wifi.setOnCheckedChangeListener((b, on) -> { prefs.edit().putBoolean("wifi-only", on).apply(); DriveWorker.schedule(this); });
        c.addView(wifi);
        Switch charging = ui.toggle(getString(R.string.drive_charging), prefs.getBoolean("charging-only", false));
        charging.setOnCheckedChangeListener((b, on) -> { prefs.edit().putBoolean("charging-only", on).apply(); DriveWorker.schedule(this); });
        c.addView(charging);

        TextView l = ui.text(getString(R.string.drive_max_file), 14);
        l.setPadding(0, ui.dp(10), 0, 0);
        c.addView(l);
        EditText max = ui.field("500", InputType.TYPE_CLASS_NUMBER);
        int mb = settings != null ? settings.optInt("maxFileMB", 0) : 0;
        max.setText(String.valueOf(mb > 0 ? mb : 500));
        c.addView(max);
        boolean noFolders = settings == null || settings.optJSONArray("folders") == null
                || settings.optJSONArray("folders").length() == 0;
        EditText dev = null;
        if (noFolders) {
            c.addView(ui.text(getString(R.string.drive_device), 14));
            dev = ui.field(getString(R.string.drive_device), InputType.TYPE_CLASS_TEXT);
            dev.setText(settings != null ? settings.optString("device") : "");
            c.addView(dev);
        }
        EditText devField = dev;
        c.addView(ui.row(ui.primary(getString(R.string.drive_save), v -> {
            JSONObject p = new JSONObject();
            try {
                p.put("maxFileMB", Integer.parseInt(max.getText().toString().trim()));
                if (devField != null) p.put("device", devField.getText().toString().trim());
            } catch (Exception e) {
                return;
            }
            unfocus();
            call(d -> d.setPrefs(p.toString()));
        })));
    }

    private void renderAccount() {
        LinearLayout c = ui.card(body);
        c.addView(ui.heading(getString(R.string.drive_account)));
        c.addView(ui.text(getString(R.string.drive_signed_in, status.optString("account")), 14));
        c.addView(ui.dim(getString(R.string.drive_trash)));
        c.addView(ui.row(
                ui.secondary(getString(R.string.drive_lock), v -> call(Drive::lock)),
                ui.danger(getString(R.string.drive_logout), v -> logout())));
    }

    private void logout() {
        new AlertDialog.Builder(this)
                .setMessage(R.string.drive_logout_ask)
                .setPositiveButton(R.string.drive_logout, (d, w) -> { DriveWorker.cancel(this); call(Drive::logout); })
                .setNegativeButton(R.string.drive_cancel, null)
                .show();
    }

    @Override
    public void onRequestPermissionsResult(int code, String[] perms, int[] results) {
        super.onRequestPermissionsResult(code, perms, results);
        if (code != MEDIA) return;
        if (CameraUploader.allowed(this)) findMedia();
        else Toast.makeText(this, R.string.drive_camera_denied, Toast.LENGTH_LONG).show();
        render();
    }

    private void renderEvents() {
        JSONArray ev = status.optJSONArray("events");
        if (ev == null || ev.length() == 0) return;
        LinearLayout c = ui.card(body);
        c.addView(ui.heading(getString(R.string.drive_activity)));
        StringBuilder sb = new StringBuilder();
        for (int i = ev.length() - 1, n = 0; i >= 0 && n < 30; i--, n++) {
            JSONObject e = ev.optJSONObject(i);
            if (e == null) continue;
            sb.append(e.optString("kind")).append(" · ").append(e.optString("folder")).append('/').append(e.optString("path"));
            if ("error".equals(e.optString("kind"))) sb.append(" (").append(e.optString("detail")).append(')');
            sb.append('\n');
        }
        c.addView(ui.dim(sb.toString().trim()));
    }
}
