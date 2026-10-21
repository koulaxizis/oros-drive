package online.useoros.drive.app;

import android.app.Activity;
import android.app.AlertDialog;
import android.content.Context;
import android.content.Intent;
import android.content.SharedPreferences;
import android.net.Uri;
import android.os.Bundle;
import android.os.Handler;
import android.os.Looper;
import android.provider.DocumentsContract;
import android.text.InputType;
import android.text.format.DateUtils;
import android.util.TypedValue;
import android.view.Gravity;
import android.view.View;
import android.widget.Button;
import android.widget.CheckBox;
import android.widget.EditText;
import android.widget.LinearLayout;
import android.widget.ScrollView;
import android.widget.TextView;
import android.widget.Toast;

import androidx.browser.customtabs.CustomTabsIntent;

import org.json.JSONArray;
import org.json.JSONObject;

import java.text.SimpleDateFormat;
import java.util.Date;
import java.util.Locale;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;

import online.useoros.drive.mobile.Drive;

/**
 * orOS Drive for Android (online.useoros.drive): sign in (Dropbox,
 * Nextcloud or any WebDAV folder), unlock, pick folders to mirror,
 * browse the orOS disk, see each folder's state. Every call into the
 * Go core runs off the main thread.
 */
public class MainActivity extends Activity {
    private static final int PICK = 41;
    private final ExecutorService io = Executors.newSingleThreadExecutor();
    private final Handler main = new Handler(Looper.getMainLooper());
    private Drive drive;
    private LinearLayout body;
    private JSONObject status = new JSONObject();
    private boolean askCreate;
    private String unlockMsg = "";
    private Boolean shown; // the Files app root was last told this
    private long cacheBytes;
    private final Runnable poll = new Runnable() {
        @Override public void run() { refresh(); main.postDelayed(this, 3000); }
    };

    @Override
    protected void onCreate(Bundle b) {
        super.onCreate(b);
        setTitle(R.string.drive_title);
        ScrollView sv = new ScrollView(this);
        body = new LinearLayout(this);
        body.setOrientation(LinearLayout.VERTICAL);
        int pad = dp(16);
        body.setPadding(pad, pad, pad, pad);
        // Taking the focus from a field lets the screen re-render.
        body.setFocusableInTouchMode(true);
        sv.addView(body);
        setContentView(sv);
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
                    body.addView(text(getString(R.string.drive_unavailable, String.valueOf(e.getMessage())), 15));
                });
            }
        });
    }

    @Override protected void onResume() { super.onResume(); main.post(poll); }
    @Override protected void onPause() { super.onPause(); main.removeCallbacks(poll); }
    @Override protected void onDestroy() { super.onDestroy(); io.shutdown(); }

    private int dp(int v) {
        return (int) TypedValue.applyDimension(TypedValue.COMPLEX_UNIT_DIP, v, getResources().getDisplayMetrics());
    }

    private TextView text(CharSequence s, int sp) {
        TextView t = new TextView(this);
        t.setText(s);
        t.setTextSize(sp);
        t.setPadding(0, dp(4), 0, dp(4));
        return t;
    }

    private TextView heading(int res) {
        TextView t = text(getString(res), 18);
        t.setTypeface(android.graphics.Typeface.DEFAULT_BOLD);
        t.setPadding(0, dp(16), 0, dp(4));
        return t;
    }

    private Button button(int res, View.OnClickListener l) {
        Button b = new Button(this);
        b.setText(res);
        b.setAllCaps(false);
        b.setOnClickListener(l);
        return b;
    }

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

    private void render() {
        // Keep a field being typed in: rebuild only when nothing has focus.
        View f = getCurrentFocus();
        if (f instanceof EditText) return;
        body.removeAllViews();
        JSONObject settings = status.optJSONObject("settings");
        String device = settings != null ? settings.optString("device") : "";
        body.addView(text(getString(R.string.drive_intro, device), 15));

        if (!status.optBoolean("signedIn")) {
            renderLogin();
            return;
        }
        body.addView(text(getString(R.string.drive_signed_in, status.optString("account")), 13));
        if (!status.optBoolean("unlocked")) {
            renderUnlock();
            return;
        }
        renderSync();
        body.addView(button(R.string.drive_browse, v -> startActivity(new Intent(this, BrowseActivity.class))));
        renderFolders(settings);
        renderFiles();
        renderOptions(settings);
        renderEvents();
    }

    private String loginMsg = "";
    private EditText davUrl, davDir, davUser, davPass;

    /** Dropbox, Nextcloud (Login Flow v2) or a WebDAV folder with an
     *  app password. The server is checked before anything is saved. */
    private void renderLogin() {
        body.addView(heading(R.string.drive_login));
        body.addView(text(getString(R.string.drive_login_text), 15));
        body.addView(button(R.string.drive_login, v -> login()));

        body.addView(heading(R.string.drive_or_server));
        body.addView(text(getString(R.string.drive_server_text), 14));
        davUrl = field(davUrl, R.string.drive_server_url, InputType.TYPE_CLASS_TEXT | InputType.TYPE_TEXT_VARIATION_URI);
        davDir = field(davDir, R.string.drive_server_dir, InputType.TYPE_CLASS_TEXT);
        body.addView(button(R.string.drive_nextcloud, v -> nextcloud()));
        TextView other = heading(R.string.drive_other_server);
        other.setTextSize(16);
        body.addView(other);
        davUser = field(davUser, R.string.drive_server_user, InputType.TYPE_CLASS_TEXT);
        davPass = field(davPass, R.string.drive_server_pass, InputType.TYPE_CLASS_TEXT | InputType.TYPE_TEXT_VARIATION_PASSWORD);
        body.addView(button(R.string.drive_connect, v -> webdav()));

        String login = status.optString("login");
        String m = loginMsg;
        if (m.isEmpty() && "waiting".equals(login)) m = getString(R.string.drive_login_wait);
        else if (m.isEmpty() && !login.isEmpty()) m = davText(login);
        if (!m.isEmpty()) body.addView(text(m, 14));
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
    private EditText field(EditText old, int hint, int type) {
        EditText e = new EditText(this);
        e.setHint(hint);
        e.setInputType(type);
        if (old != null) e.setText(old.getText());
        body.addView(e);
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
        body.addView(heading(R.string.drive_unlock));
        body.addView(text(getString("reset".equals(status.optString("needsKey"))
                ? R.string.drive_unlock_reset : R.string.drive_unlock_text), 15));
        EditText pass = new EditText(this);
        pass.setHint(R.string.drive_passphrase);
        pass.setInputType(InputType.TYPE_CLASS_TEXT | InputType.TYPE_TEXT_VARIATION_PASSWORD);
        body.addView(pass);
        body.addView(button(R.string.drive_unlock, v -> unlock(pass, false)));
        if (askCreate) {
            body.addView(text(getString(R.string.drive_no_vault), 14));
            body.addView(button(R.string.drive_create, v -> unlock(pass, true)));
        }
        if (!unlockMsg.isEmpty()) body.addView(text(unlockMsg, 14));
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

    private void renderSync() {
        LinearLayout row = new LinearLayout(this);
        row.setOrientation(LinearLayout.HORIZONTAL);
        row.setGravity(Gravity.CENTER_VERTICAL);
        boolean paused = status.optBoolean("paused");
        row.addView(button(R.string.drive_sync_now, v -> { call(d -> d.sync()); }));
        row.addView(button(paused ? R.string.drive_resume : R.string.drive_pause, v -> call(d -> d.setPaused(!paused))));
        body.addView(row);
        String line;
        if (status.optBoolean("syncing")) line = getString(R.string.drive_syncing);
        else if (paused) line = getString(R.string.drive_paused);
        else line = getString(R.string.drive_last, when(status.optString("lastSync")));
        if (!status.optString("error").isEmpty()) line += "\n" + getString(R.string.drive_error, status.optString("error"));
        body.addView(text(line, 14));
    }

    private String when(String iso) {
        if (iso.isEmpty() || iso.startsWith("0001-")) return "–";
        try {
            SimpleDateFormat f = new SimpleDateFormat("yyyy-MM-dd'T'HH:mm:ss", Locale.ROOT);
            f.setTimeZone(java.util.TimeZone.getTimeZone("UTC"));
            Date d = f.parse(iso.substring(0, 19));
            if (d == null) return iso;
            return DateUtils.getRelativeTimeSpanString(d.getTime()).toString();
        } catch (Exception e) {
            return iso;
        }
    }

    private void renderFolders(JSONObject settings) {
        body.addView(heading(R.string.drive_folders));
        JSONArray folders = settings != null ? settings.optJSONArray("folders") : null;
        JSONObject states = status.optJSONObject("folders");
        if (folders == null || folders.length() == 0) body.addView(text(getString(R.string.drive_none), 14));
        for (int i = 0; folders != null && i < folders.length(); i++) {
            JSONObject p = folders.optJSONObject(i);
            if (p == null) continue;
            String id = p.optString("id");
            JSONObject f = states != null ? states.optJSONObject(id) : null;
            if (f == null) f = new JSONObject();
            String remote = p.optString("remote");
            TextView name = text(remote.substring(remote.lastIndexOf('/') + 1), 16);
            name.setTypeface(android.graphics.Typeface.DEFAULT_BOLD);
            body.addView(name);
            body.addView(text(folderLabel(p.optString("local")) + "\norOS › " + remote.replaceFirst("^/internal/", "").replace("/", " › "), 13));
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
            body.addView(text(line, 14));
            LinearLayout row = new LinearLayout(this);
            row.setOrientation(LinearLayout.HORIZONTAL);
            if ("held".equals(state) && !paused) {
                row.addView(button(R.string.drive_approve, v -> call(d -> { d.approveDeletes(id); d.sync(); })));
                row.addView(button(R.string.drive_hold, v -> call(d -> d.pauseFolder(id, true))));
            } else {
                row.addView(button(paused ? R.string.drive_resume : R.string.drive_pause, v -> call(d -> d.pauseFolder(id, !paused))));
                row.addView(button(R.string.drive_remove, v -> new AlertDialog.Builder(this)
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
                        .show()));
            }
            body.addView(row);
        }
        body.addView(button(R.string.drive_add, v -> {
            Intent i = new Intent(Intent.ACTION_OPEN_DOCUMENT_TREE);
            i.addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION | Intent.FLAG_GRANT_WRITE_URI_PERMISSION
                    | Intent.FLAG_GRANT_PERSISTABLE_URI_PERMISSION);
            startActivityForResult(i, PICK);
        }));
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
        EditText name = new EditText(this);
        name.setText(label.contains("/") ? label.substring(label.lastIndexOf('/') + 1) : label);
        new AlertDialog.Builder(this)
                .setTitle(R.string.drive_name_title)
                .setView(name)
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
        body.addView(heading(R.string.drive_files_title));
        body.addView(text(getString(R.string.drive_files_text), 14));
        body.addView(text(getString(R.string.drive_cache,
                android.text.format.Formatter.formatShortFileSize(this, cacheBytes)), 13));
        if (cacheBytes > 0) body.addView(button(R.string.drive_clear_cache, v -> call(Drive::diskClearCache)));
    }

    private void renderOptions(JSONObject settings) {
        body.addView(heading(R.string.drive_options));
        SharedPreferences prefs = getSharedPreferences(DriveWorker.PREFS, Context.MODE_PRIVATE);
        CheckBox wifi = new CheckBox(this);
        wifi.setText(R.string.drive_wifi);
        wifi.setChecked(prefs.getBoolean("wifi-only", true));
        wifi.setOnCheckedChangeListener((b, on) -> { prefs.edit().putBoolean("wifi-only", on).apply(); DriveWorker.schedule(this); });
        body.addView(wifi);
        CheckBox charging = new CheckBox(this);
        charging.setText(R.string.drive_charging);
        charging.setChecked(prefs.getBoolean("charging-only", false));
        charging.setOnCheckedChangeListener((b, on) -> { prefs.edit().putBoolean("charging-only", on).apply(); DriveWorker.schedule(this); });
        body.addView(charging);
        // Camera backup (wave 4): only what is taken from now on.
        CheckBox camera = new CheckBox(this);
        camera.setText(R.string.drive_camera);
        camera.setChecked(prefs.getBoolean(CameraUploader.ON, false) && CameraUploader.allowed(this));
        camera.setOnCheckedChangeListener((b, on) -> {
            if (!on) { prefs.edit().putBoolean(CameraUploader.ON, false).apply(); return; }
            if (CameraUploader.allowed(this)) cameraOn();
            else requestPermissions(CameraUploader.permissions(), 2);
        });
        body.addView(camera);
        if (prefs.getBoolean(CameraUploader.ON, false)) {
            body.addView(text(getString(R.string.drive_camera_where), 13));
        }

        body.addView(text(getString(R.string.drive_max_file), 14));
        EditText max = new EditText(this);
        max.setInputType(InputType.TYPE_CLASS_NUMBER);
        int mb = settings != null ? settings.optInt("maxFileMB", 0) : 0;
        max.setText(String.valueOf(mb > 0 ? mb : 500));
        body.addView(max);
        boolean noFolders = settings == null || settings.optJSONArray("folders") == null
                || settings.optJSONArray("folders").length() == 0;
        EditText dev = null;
        if (noFolders) {
            body.addView(text(getString(R.string.drive_device), 14));
            dev = new EditText(this);
            dev.setText(settings != null ? settings.optString("device") : "");
            body.addView(dev);
        }
        EditText devField = dev;
        body.addView(button(R.string.drive_save, v -> {
            JSONObject p = new JSONObject();
            try {
                p.put("maxFileMB", Integer.parseInt(max.getText().toString().trim()));
                if (devField != null) p.put("device", devField.getText().toString().trim());
            } catch (Exception e) {
                return;
            }
            max.clearFocus();
            if (devField != null) devField.clearFocus();
            call(d -> d.setPrefs(p.toString()));
        }));
        body.addView(text(getString(R.string.drive_trash), 13));
        LinearLayout row = new LinearLayout(this);
        row.setOrientation(LinearLayout.HORIZONTAL);
        row.addView(button(R.string.drive_lock, v -> call(Drive::lock)));
        row.addView(button(R.string.drive_logout, v -> new AlertDialog.Builder(this)
                .setMessage(R.string.drive_logout_ask)
                .setPositiveButton(R.string.drive_logout, (d, w) -> { DriveWorker.cancel(this); call(Drive::logout); })
                .setNegativeButton(R.string.drive_cancel, null)
                .show()));
        body.addView(row);
    }

    private void cameraOn() {
        getSharedPreferences(DriveWorker.PREFS, Context.MODE_PRIVATE).edit()
                .putBoolean(CameraUploader.ON, true)
                .putLong(CameraUploader.SINCE, System.currentTimeMillis() / 1000)
                .remove(CameraUploader.DONE)
                .apply();
        DriveWorker.schedule(this);
        render();
    }

    @Override
    public void onRequestPermissionsResult(int code, String[] perms, int[] results) {
        super.onRequestPermissionsResult(code, perms, results);
        if (code != 2) return;
        if (CameraUploader.allowed(this)) cameraOn();
        else {
            Toast.makeText(this, R.string.drive_camera_denied, Toast.LENGTH_LONG).show();
            render();
        }
    }

    private void renderEvents() {
        JSONArray ev = status.optJSONArray("events");
        if (ev == null || ev.length() == 0) return;
        body.addView(heading(R.string.drive_activity));
        StringBuilder sb = new StringBuilder();
        for (int i = ev.length() - 1, n = 0; i >= 0 && n < 30; i--, n++) {
            JSONObject e = ev.optJSONObject(i);
            if (e == null) continue;
            sb.append(e.optString("kind")).append(" · ").append(e.optString("folder")).append('/').append(e.optString("path"));
            if ("error".equals(e.optString("kind"))) sb.append(" (").append(e.optString("detail")).append(')');
            sb.append('\n');
        }
        body.addView(text(sb.toString(), 12));
    }
}
