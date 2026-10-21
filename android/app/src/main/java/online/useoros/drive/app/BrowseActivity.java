package online.useoros.drive.app;

import android.app.Activity;
import android.content.ActivityNotFoundException;
import android.content.Intent;
import android.net.Uri;
import android.os.Bundle;
import android.os.Handler;
import android.os.Looper;
import android.provider.DocumentsContract;
import android.text.format.Formatter;
import android.util.TypedValue;
import android.view.View;
import android.widget.Button;
import android.widget.LinearLayout;
import android.widget.ScrollView;
import android.widget.TextView;
import android.widget.Toast;

import org.json.JSONArray;
import org.json.JSONObject;

import java.util.ArrayList;
import java.util.Collections;
import java.util.List;
import java.util.Locale;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;

import online.useoros.drive.mobile.Drive;

/**
 * The orOS disk, folder by folder. A file opens in the app the phone
 * picks for it, through DriveProvider: it downloads (hash-checked) when
 * that app reads it, and a save from that app uploads it again, with a
 * conflict copy if it changed in orOS meanwhile.
 */
public class BrowseActivity extends Activity {
    private static final String ROOT = "/internal";
    private final ExecutorService io = Executors.newSingleThreadExecutor();
    private final Handler main = new Handler(Looper.getMainLooper());
    private LinearLayout body;
    private String path = ROOT;

    @Override
    protected void onCreate(Bundle b) {
        super.onCreate(b);
        if (b != null) path = b.getString("path", ROOT);
        ScrollView sv = new ScrollView(this);
        body = new LinearLayout(this);
        body.setOrientation(LinearLayout.VERTICAL);
        int pad = dp(16);
        body.setPadding(pad, pad, pad, pad);
        sv.addView(body);
        setContentView(sv);
        load(false);
    }

    @Override protected void onSaveInstanceState(Bundle out) { super.onSaveInstanceState(out); out.putString("path", path); }
    @Override protected void onDestroy() { super.onDestroy(); io.shutdown(); }

    @Override
    public void onBackPressed() {
        if (!ROOT.equals(path)) { path = path.substring(0, path.lastIndexOf('/')); load(false); }
        else super.onBackPressed();
    }

    private int dp(int v) {
        return (int) TypedValue.applyDimension(TypedValue.COMPLEX_UNIT_DIP, v, getResources().getDisplayMetrics());
    }

    private void load(boolean fresh) {
        String at = path;
        setTitle(ROOT.equals(at) ? "orOS" : at.substring(at.lastIndexOf('/') + 1));
        io.execute(() -> {
            try {
                Drive d = DriveCore.get(this);
                if (fresh) d.diskRefresh();
                JSONArray list = new JSONArray(d.diskList(at));
                List<JSONObject> items = new ArrayList<>();
                for (int i = 0; i < list.length(); i++) items.add(list.getJSONObject(i));
                // Folders first, then by name.
                Collections.sort(items, (x, y) -> {
                    boolean dx = x.optBoolean("dir"), dy = y.optBoolean("dir");
                    if (dx != dy) return dx ? -1 : 1;
                    return x.optString("name").toLowerCase(Locale.ROOT).compareTo(y.optString("name").toLowerCase(Locale.ROOT));
                });
                main.post(() -> { if (at.equals(path)) show(items); });
            } catch (Exception e) {
                main.post(() -> {
                    body.removeAllViews();
                    body.addView(line(String.valueOf(e.getMessage()), 15));
                });
            }
        });
    }

    private TextView line(CharSequence s, int sp) {
        TextView t = new TextView(this);
        t.setText(s);
        t.setTextSize(sp);
        t.setPadding(0, dp(10), 0, dp(10));
        return t;
    }

    private void show(List<JSONObject> items) {
        body.removeAllViews();
        LinearLayout row = new LinearLayout(this);
        row.setOrientation(LinearLayout.HORIZONTAL);
        if (!ROOT.equals(path)) row.addView(button(R.string.drive_up, v -> onBackPressed()));
        row.addView(button(R.string.drive_refresh, v -> load(true)));
        body.addView(row);
        TextView where = line("orOS" + path.substring(ROOT.length()).replace("/", " › "), 13);
        body.addView(where);
        if (items.isEmpty()) body.addView(line(getString(R.string.drive_browse_empty), 15));
        for (JSONObject i : items) {
            boolean dir = i.optBoolean("dir");
            String name = i.optString("name");
            String p = i.optString("path");
            CharSequence label = (dir ? "📁 " : "📄 ") + name
                    + (dir ? "" : "  ·  " + Formatter.formatShortFileSize(this, i.optLong("size")));
            TextView t = line(label, 16);
            t.setBackgroundResource(android.R.drawable.list_selector_background);
            t.setOnClickListener(v -> {
                if (dir) { path = p; load(false); }
                else open(i);
            });
            body.addView(t);
        }
    }

    private Button button(int res, View.OnClickListener l) {
        Button b = new Button(this);
        b.setText(res);
        b.setAllCaps(false);
        b.setOnClickListener(l);
        return b;
    }

    /** Hands the file to another app as a DriveProvider document, which
     *  that app may read and save back. */
    private void open(JSONObject item) {
        String name = item.optString("name");
        Toast.makeText(this, getString(R.string.drive_opening, name), Toast.LENGTH_SHORT).show();
        Uri u = DocumentsContract.buildDocumentUri(DriveProvider.authority(this), item.optString("path"));
        Intent i = new Intent(Intent.ACTION_VIEW);
        i.setDataAndType(u, DriveProvider.mime(item));
        i.addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION | Intent.FLAG_GRANT_WRITE_URI_PERMISSION);
        try {
            startActivity(Intent.createChooser(i, name));
        } catch (ActivityNotFoundException e) {
            Toast.makeText(this, R.string.drive_no_app, Toast.LENGTH_LONG).show();
        }
    }
}
