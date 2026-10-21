package online.useoros.drive.app;

import android.app.Activity;
import android.content.Context;
import android.content.Intent;
import android.content.SharedPreferences;
import android.net.Uri;
import android.os.Bundle;
import android.os.Handler;
import android.os.Looper;
import android.os.Parcelable;
import android.util.TypedValue;
import android.view.View;
import android.widget.Button;
import android.widget.LinearLayout;
import android.widget.ScrollView;
import android.widget.TextView;
import android.widget.Toast;

import org.json.JSONArray;
import org.json.JSONObject;

import java.io.ByteArrayInputStream;
import java.io.File;
import java.io.InputStream;
import java.nio.charset.StandardCharsets;
import java.text.SimpleDateFormat;
import java.util.ArrayList;
import java.util.Date;
import java.util.List;
import java.util.Locale;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;

import online.useoros.drive.mobile.Drive;

/**
 * "Share › orOS Drive" from any app (wave 4): one or more files, or a
 * text. They are copied into the outbox right away, the person picks an
 * orOS folder, and UploadWorker sends them in the background. Nothing
 * in orOS is replaced (PutNew) and nothing on the phone is deleted.
 */
public class ShareActivity extends Activity {
    private static final String ROOT = "/internal";
    private final ExecutorService io = Executors.newSingleThreadExecutor();
    private final Handler main = new Handler(Looper.getMainLooper());
    private final List<File> items = new ArrayList<>();
    private volatile boolean queued;
    private LinearLayout body;
    private String path = ROOT;

    @Override
    protected void onCreate(Bundle b) {
        super.onCreate(b);
        setTitle(R.string.drive_share_title);
        ScrollView sv = new ScrollView(this);
        body = new LinearLayout(this);
        body.setOrientation(LinearLayout.VERTICAL);
        int pad = dp(16);
        body.setPadding(pad, pad, pad, pad);
        sv.addView(body);
        setContentView(sv);
        body.addView(line(getString(R.string.drive_share_copying), 15));
        path = getSharedPreferences(DriveWorker.PREFS, Context.MODE_PRIVATE).getString("share-dest", ROOT);
        Intent in = getIntent();
        io.execute(() -> {
            try {
                take(in);
                if (items.isEmpty()) throw new Exception(getString(R.string.drive_share_nothing));
                Drive d = DriveCore.get(this);
                JSONObject st = new JSONObject(d.status());
                if (!st.optBoolean("signedIn") || !st.optBoolean("unlocked")) {
                    main.post(this::locked);
                    return;
                }
                main.post(() -> load(path));
            } catch (Exception e) {
                main.post(() -> {
                    body.removeAllViews();
                    body.addView(line(String.valueOf(e.getMessage()), 15));
                });
            }
        });
    }

    @Override
    protected void onDestroy() {
        super.onDestroy();
        io.shutdown();
        // A share that was not sent leaves nothing behind.
        if (!queued) for (File f : items) Outbox.delete(f);
    }

    private int dp(int v) {
        return (int) TypedValue.applyDimension(TypedValue.COMPLEX_UNIT_DIP, v, getResources().getDisplayMetrics());
    }

    private void take(Intent in) throws Exception {
        List<Uri> uris = new ArrayList<>();
        if (Intent.ACTION_SEND_MULTIPLE.equals(in.getAction())) {
            ArrayList<Parcelable> l = in.getParcelableArrayListExtra(Intent.EXTRA_STREAM);
            if (l != null) for (Parcelable p : l) if (p instanceof Uri) uris.add((Uri) p);
        } else {
            Parcelable p = in.getParcelableExtra(Intent.EXTRA_STREAM);
            if (p instanceof Uri) uris.add((Uri) p);
        }
        for (Uri u : uris) {
            try (InputStream s = getContentResolver().openInputStream(u)) {
                if (s != null) items.add(Outbox.take(this, s, Outbox.name(getContentResolver(), u)));
            }
        }
        CharSequence text = in.getCharSequenceExtra(Intent.EXTRA_TEXT);
        if (uris.isEmpty() && text != null && text.length() > 0) {
            String name = "Note " + new SimpleDateFormat("yyyy-MM-dd HHmm", Locale.ROOT).format(new Date()) + ".txt";
            items.add(Outbox.take(this, new ByteArrayInputStream(text.toString().getBytes(StandardCharsets.UTF_8)), name));
        }
    }

    private TextView line(CharSequence s, int sp) {
        TextView t = new TextView(this);
        t.setText(s);
        t.setTextSize(sp);
        t.setPadding(0, dp(10), 0, dp(10));
        return t;
    }

    private Button button(CharSequence label, View.OnClickListener l) {
        Button b = new Button(this);
        b.setText(label);
        b.setAllCaps(false);
        b.setOnClickListener(l);
        return b;
    }

    private void locked() {
        body.removeAllViews();
        body.addView(line(getString(R.string.drive_share_locked), 15));
        body.addView(button(getString(R.string.drive_title), v -> {
            startActivity(new Intent(this, MainActivity.class));
            finish();
        }));
    }

    /** Folder picker: the subfolders of at, Up, and "Upload here". */
    private void load(String at) {
        io.execute(() -> {
            try {
                JSONArray list = new JSONArray(DriveCore.get(this).diskList(at));
                List<JSONObject> dirs = new ArrayList<>();
                for (int i = 0; i < list.length(); i++) {
                    JSONObject o = list.getJSONObject(i);
                    if (o.optBoolean("dir")) dirs.add(o);
                }
                main.post(() -> { path = at; show(dirs); });
            } catch (Exception e) {
                // The folder last used is gone: start at the top.
                if (!ROOT.equals(at)) main.post(() -> load(ROOT));
                else main.post(() -> Toast.makeText(this, String.valueOf(e.getMessage()), Toast.LENGTH_LONG).show());
            }
        });
    }

    private void show(List<JSONObject> dirs) {
        body.removeAllViews();
        body.addView(line(getResources().getQuantityString(R.plurals.drive_share_count, items.size(), items.size()), 15));
        body.addView(line(UploadWorker.where(path), 13));
        LinearLayout row = new LinearLayout(this);
        row.setOrientation(LinearLayout.HORIZONTAL);
        if (!ROOT.equals(path)) row.addView(button(getString(R.string.drive_up), v -> load(path.substring(0, path.lastIndexOf('/')))));
        row.addView(button(getString(R.string.drive_share_here), v -> send()));
        body.addView(row);
        for (JSONObject o : dirs) {
            TextView t = line("📁 " + o.optString("name"), 16);
            t.setBackgroundResource(android.R.drawable.list_selector_background);
            String p = o.optString("path");
            t.setOnClickListener(v -> load(p));
            body.addView(t);
        }
    }

    private void send() {
        String dest = path;
        io.execute(() -> {
            try {
                Outbox.queue(items, dest);
                queued = true;
                getSharedPreferences(DriveWorker.PREFS, Context.MODE_PRIVATE).edit().putString("share-dest", dest).apply();
                UploadWorker.start(this);
                main.post(() -> {
                    Toast.makeText(this, getString(R.string.drive_share_queued, UploadWorker.where(dest)), Toast.LENGTH_LONG).show();
                    finish();
                });
            } catch (Exception e) {
                main.post(() -> Toast.makeText(this, String.valueOf(e.getMessage()), Toast.LENGTH_LONG).show());
            }
        });
    }
}
