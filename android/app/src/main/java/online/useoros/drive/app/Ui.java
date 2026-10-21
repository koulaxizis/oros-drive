package online.useoros.drive.app;

import android.app.Activity;
import android.content.res.ColorStateList;
import android.content.res.Configuration;
import android.graphics.Color;
import android.graphics.Typeface;
import android.graphics.drawable.Drawable;
import android.graphics.drawable.GradientDrawable;
import android.graphics.drawable.RippleDrawable;
import android.os.Build;
import android.text.TextUtils;
import android.util.TypedValue;
import android.view.Gravity;
import android.view.View;
import android.view.ViewGroup;
import android.view.Window;
import android.widget.Button;
import android.widget.EditText;
import android.widget.ImageView;
import android.widget.LinearLayout;
import android.widget.ScrollView;
import android.widget.Switch;
import android.widget.TextView;

/**
 * The look every screen shares, after orOS's own (style.css, Adwaita
 * skin): its light or dark palette with the phone's setting, a top bar,
 * content in rounded panels, filled accent buttons. The screen is laid
 * out around the status and navigation bars (Android 15 draws apps
 * under them), so nothing hides behind them.
 */
final class Ui {
    final Activity a;
    final int bg, panel, text, dim, accent, soft, border, danger;
    /** The scrolling column the screen fills. */
    final LinearLayout body;
    private final TextView title;
    private final LinearLayout bar;

    Ui(Activity a, CharSequence titleText, boolean back) {
        this.a = a;
        bg = a.getColor(R.color.bg);
        panel = a.getColor(R.color.panel);
        text = a.getColor(R.color.text);
        dim = a.getColor(R.color.dim);
        accent = a.getColor(R.color.accent);
        soft = a.getColor(R.color.accent_soft);
        border = a.getColor(R.color.border);
        danger = a.getColor(R.color.danger);

        LinearLayout root = new LinearLayout(a);
        root.setOrientation(LinearLayout.VERTICAL);
        root.setBackgroundColor(bg);

        bar = new LinearLayout(a);
        bar.setOrientation(LinearLayout.HORIZONTAL);
        bar.setGravity(Gravity.CENTER_VERTICAL);
        bar.setPadding(dp(back ? 4 : 16), dp(10), dp(16), dp(10));
        if (back) {
            TextView up = new TextView(a);
            up.setText("‹");
            up.setTextSize(30);
            up.setTextColor(text);
            up.setGravity(Gravity.CENTER);
            up.setContentDescription(a.getString(R.string.drive_back));
            up.setBackground(ripple(Color.TRANSPARENT, dp(20)));
            up.setOnClickListener(v -> a.onBackPressed());
            bar.addView(up, new LinearLayout.LayoutParams(dp(44), dp(44)));
        } else {
            ImageView icon = new ImageView(a);
            icon.setImageResource(R.mipmap.ic_launcher);
            LinearLayout.LayoutParams lp = new LinearLayout.LayoutParams(dp(32), dp(32));
            lp.rightMargin = dp(12);
            bar.addView(icon, lp);
        }
        title = new TextView(a);
        title.setText(titleText);
        title.setTextSize(20);
        title.setTextColor(text);
        title.setTypeface(Typeface.create("sans-serif-medium", Typeface.NORMAL));
        title.setSingleLine(true);
        title.setEllipsize(TextUtils.TruncateAt.MIDDLE);
        bar.addView(title, new LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1));
        root.addView(bar);
        View line = new View(a);
        line.setBackgroundColor(border);
        root.addView(line, new LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, Math.max(1, dp(1) / 2)));

        ScrollView sv = new ScrollView(a);
        sv.setFillViewport(true);
        sv.setClipToPadding(false);
        body = new LinearLayout(a);
        body.setOrientation(LinearLayout.VERTICAL);
        body.setPadding(dp(12), dp(12), dp(12), dp(24));
        // Taking the focus from a field lets the screen re-render.
        body.setFocusableInTouchMode(true);
        sv.addView(body);
        root.addView(sv, new LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, 0, 1));
        a.setContentView(root);
        edgeToEdge(root);
    }

    /** Draws under transparent system bars and pads the screen by
     *  their size (and the keyboard's), on every Android version. */
    @SuppressWarnings("deprecation")
    private void edgeToEdge(View root) {
        Window w = a.getWindow();
        boolean night = (a.getResources().getConfiguration().uiMode & Configuration.UI_MODE_NIGHT_MASK)
                == Configuration.UI_MODE_NIGHT_YES;
        int flags = View.SYSTEM_UI_FLAG_LAYOUT_STABLE | View.SYSTEM_UI_FLAG_LAYOUT_FULLSCREEN
                | View.SYSTEM_UI_FLAG_LAYOUT_HIDE_NAVIGATION;
        if (!night) {
            flags |= View.SYSTEM_UI_FLAG_LIGHT_STATUS_BAR;
            if (Build.VERSION.SDK_INT >= 26) flags |= View.SYSTEM_UI_FLAG_LIGHT_NAVIGATION_BAR;
        }
        w.getDecorView().setSystemUiVisibility(flags);
        w.setStatusBarColor(Color.TRANSPARENT);
        // Before 8.0 the navigation icons cannot turn dark: keep a bar.
        w.setNavigationBarColor(night || Build.VERSION.SDK_INT >= 26 ? Color.TRANSPARENT : Color.BLACK);
        if (Build.VERSION.SDK_INT >= 29) w.setNavigationBarContrastEnforced(false);
        root.setOnApplyWindowInsetsListener((v, in) -> {
            v.setPadding(in.getSystemWindowInsetLeft(), in.getSystemWindowInsetTop(),
                    in.getSystemWindowInsetRight(), in.getSystemWindowInsetBottom());
            return in.consumeSystemWindowInsets();
        });
        root.requestApplyInsets();
    }

    void setTitle(CharSequence s) { title.setText(s); }

    int dp(int v) {
        return (int) TypedValue.applyDimension(TypedValue.COMPLEX_UNIT_DIP, v, a.getResources().getDisplayMetrics());
    }

    GradientDrawable shape(int fill, int radius, int stroke) {
        GradientDrawable g = new GradientDrawable();
        g.setColor(fill);
        g.setCornerRadius(radius);
        if (stroke != 0) g.setStroke(Math.max(1, dp(1)), stroke);
        return g;
    }

    Drawable ripple(int fill, int radius) {
        return new RippleDrawable(ColorStateList.valueOf(soft), fill == Color.TRANSPARENT ? null : shape(fill, radius, 0),
                shape(Color.WHITE, radius, 0));
    }

    /** A rounded panel added to the screen; fill it with add(). */
    LinearLayout card(LinearLayout into) {
        LinearLayout c = new LinearLayout(a);
        c.setOrientation(LinearLayout.VERTICAL);
        c.setBackground(shape(panel, dp(12), border));
        c.setPadding(dp(16), dp(14), dp(16), dp(14));
        LinearLayout.LayoutParams lp = new LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT);
        lp.bottomMargin = dp(12);
        into.addView(c, lp);
        return c;
    }

    TextView text(CharSequence s, int sp) {
        TextView t = new TextView(a);
        t.setText(s);
        t.setTextSize(sp);
        t.setTextColor(text);
        t.setLineSpacing(0, 1.1f);
        t.setPadding(0, dp(3), 0, dp(3));
        return t;
    }

    TextView dim(CharSequence s) {
        TextView t = text(s, 13);
        t.setTextColor(dim);
        return t;
    }

    TextView heading(CharSequence s) {
        TextView t = text(s, 16);
        t.setTypeface(Typeface.create("sans-serif-medium", Typeface.NORMAL));
        t.setPadding(0, 0, 0, dp(4));
        return t;
    }

    /** The main action: filled with the accent colour. */
    Button primary(CharSequence label, View.OnClickListener l) {
        Button b = plainButton(label, l);
        b.setTextColor(Color.WHITE);
        b.setBackground(ripple(accent, dp(8)));
        return b;
    }

    /** Other actions: outlined. */
    Button secondary(CharSequence label, View.OnClickListener l) {
        Button b = plainButton(label, l);
        b.setTextColor(text);
        GradientDrawable g = shape(Color.TRANSPARENT, dp(8), border);
        b.setBackground(new RippleDrawable(ColorStateList.valueOf(soft), g, shape(Color.WHITE, dp(8), 0)));
        return b;
    }

    /** Sign out and the like: outlined, red text. */
    Button danger(CharSequence label, View.OnClickListener l) {
        Button b = secondary(label, l);
        b.setTextColor(danger);
        return b;
    }

    /** A small accent-coloured text action. */
    TextView link(CharSequence label, View.OnClickListener l) {
        TextView t = text(label, 14);
        t.setTextColor(accent);
        t.setTypeface(Typeface.create("sans-serif-medium", Typeface.NORMAL));
        t.setPadding(0, dp(8), 0, dp(8));
        t.setOnClickListener(l);
        return t;
    }

    private Button plainButton(CharSequence label, View.OnClickListener l) {
        Button b = new Button(a);
        b.setText(label);
        b.setAllCaps(false);
        b.setTextSize(15);
        b.setTypeface(Typeface.create("sans-serif-medium", Typeface.NORMAL));
        b.setStateListAnimator(null);
        b.setMinHeight(dp(44));
        b.setMinimumHeight(dp(44));
        b.setPadding(dp(16), 0, dp(16), 0);
        b.setOnClickListener(l);
        return b;
    }

    /** Buttons side by side, sharing the width. */
    LinearLayout row(View... views) {
        LinearLayout r = new LinearLayout(a);
        r.setOrientation(LinearLayout.HORIZONTAL);
        for (int i = 0; i < views.length; i++) {
            LinearLayout.LayoutParams lp = new LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1);
            if (i > 0) lp.leftMargin = dp(8);
            r.addView(views[i], lp);
        }
        r.setPadding(0, dp(6), 0, dp(2));
        return r;
    }

    EditText field(CharSequence hint, int type) {
        EditText e = new EditText(a);
        e.setHint(hint);
        e.setInputType(type);
        e.setTextSize(15);
        e.setTextColor(text);
        e.setHintTextColor(dim);
        e.setBackground(shape(bg, dp(8), border));
        e.setPadding(dp(12), dp(10), dp(12), dp(10));
        LinearLayout.LayoutParams lp = new LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT);
        lp.topMargin = dp(4);
        lp.bottomMargin = dp(6);
        e.setLayoutParams(lp);
        return e;
    }

    /** An on/off option with the label on the left. */
    Switch toggle(CharSequence label, boolean on) {
        Switch s = new Switch(a);
        s.setText(label);
        s.setTextSize(15);
        s.setTextColor(text);
        s.setChecked(on);
        s.setPadding(0, dp(8), 0, dp(8));
        int[][] states = {{android.R.attr.state_checked}, {}};
        s.setThumbTintList(new ColorStateList(states, new int[]{accent, Color.WHITE}));
        s.setTrackTintList(new ColorStateList(states, new int[]{soft, border}));
        return s;
    }

    /** A tappable list row: an icon, a name and a dim second line. */
    LinearLayout item(String icon, CharSequence name, CharSequence sub, View.OnClickListener l) {
        LinearLayout r = new LinearLayout(a);
        r.setOrientation(LinearLayout.HORIZONTAL);
        r.setGravity(Gravity.CENTER_VERTICAL);
        r.setPadding(dp(8), dp(10), dp(8), dp(10));
        r.setBackground(ripple(Color.TRANSPARENT, dp(8)));
        TextView i = text(icon, 22);
        i.setGravity(Gravity.CENTER);
        r.addView(i, new LinearLayout.LayoutParams(dp(36), ViewGroup.LayoutParams.WRAP_CONTENT));
        LinearLayout col = new LinearLayout(a);
        col.setOrientation(LinearLayout.VERTICAL);
        col.setPadding(dp(10), 0, 0, 0);
        TextView n = text(name, 15);
        n.setPadding(0, 0, 0, 0);
        n.setSingleLine(true);
        n.setEllipsize(TextUtils.TruncateAt.MIDDLE);
        col.addView(n);
        if (sub != null && sub.length() > 0) {
            TextView s = dim(sub);
            s.setPadding(0, 0, 0, 0);
            col.addView(s);
        }
        r.addView(col, new LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1));
        if (l != null) r.setOnClickListener(l);
        return r;
    }
}
