package online.useoros.drive.app;

import android.app.Notification;
import android.app.NotificationChannel;
import android.app.NotificationManager;
import android.app.PendingIntent;
import android.content.Context;
import android.content.Intent;
import android.os.Build;

/** The app's notifications (channel "oros-drive"), shown only when the
 *  person allows them. */
final class Notify {
    static final String CHANNEL = "oros-drive";

    private Notify() { }

    private static NotificationManager manager(Context c) {
        NotificationManager nm = c.getSystemService(NotificationManager.class);
        if (nm == null || !nm.areNotificationsEnabled()) return null;
        if (Build.VERSION.SDK_INT >= 26) {
            nm.createNotificationChannel(new NotificationChannel(CHANNEL, c.getString(R.string.drive_channel),
                    NotificationManager.IMPORTANCE_DEFAULT));
        }
        return nm;
    }

    private static Notification.Builder builder(Context c, String title, String text) {
        PendingIntent open = PendingIntent.getActivity(c, 0, new Intent(c, MainActivity.class),
                PendingIntent.FLAG_UPDATE_CURRENT | PendingIntent.FLAG_IMMUTABLE);
        return (Build.VERSION.SDK_INT >= 26 ? new Notification.Builder(c, CHANNEL) : new Notification.Builder(c))
                .setSmallIcon(R.drawable.ic_stat_drive)
                .setContentTitle(title)
                .setContentText(text)
                .setStyle(new Notification.BigTextStyle().bigText(text))
                .setContentIntent(open);
    }

    /** A message the person may tap away. */
    static void message(Context c, String tag, int id, String title, String text, String category) {
        NotificationManager nm = manager(c);
        if (nm == null) return;
        nm.notify(tag, id, builder(c, title, text).setAutoCancel(true).setCategory(category).build());
    }

    /** An ongoing "n of max" bar; cancel() when done. */
    static void progress(Context c, String tag, int id, String title, String text, int n, int max) {
        NotificationManager nm = manager(c);
        if (nm == null) return;
        nm.notify(tag, id, builder(c, title, text).setOngoing(true).setOnlyAlertOnce(true)
                .setProgress(max, n, max == 0).setCategory(Notification.CATEGORY_PROGRESS).build());
    }

    static void cancel(Context c, String tag, int id) {
        NotificationManager nm = c.getSystemService(NotificationManager.class);
        if (nm != null) nm.cancel(tag, id);
    }
}
