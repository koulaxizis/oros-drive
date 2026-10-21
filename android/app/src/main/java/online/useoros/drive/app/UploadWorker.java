package online.useoros.drive.app;

import android.app.Notification;
import android.content.Context;

import androidx.annotation.NonNull;
import androidx.work.BackoffPolicy;
import androidx.work.Constraints;
import androidx.work.ExistingWorkPolicy;
import androidx.work.NetworkType;
import androidx.work.OneTimeWorkRequest;
import androidx.work.WorkManager;
import androidx.work.Worker;
import androidx.work.WorkerParameters;

import java.io.File;
import java.util.List;
import java.util.concurrent.TimeUnit;

import online.useoros.drive.mobile.Drive;

/**
 * Sends what was shared to orOS Drive (Outbox) in the background, with
 * a progress notification. PutNew never replaces a file in orOS; a
 * file that cannot go at all (too big, a name orOS cannot hold) is
 * dropped and named in the final notification; anything else (offline,
 * locked) waits and tries again.
 */
public class UploadWorker extends Worker {
    private static final String WORK = "oros-drive-upload";
    private static final String TAG = "upload";

    public UploadWorker(@NonNull Context c, @NonNull WorkerParameters p) { super(c, p); }

    static String where(String dest) {
        return "orOS" + dest.substring("/internal".length()).replace("/", " › ");
    }

    @NonNull @Override
    public Result doWork() {
        Context c = getApplicationContext();
        List<File> q = Outbox.queued(c);
        if (q.isEmpty()) return Result.success();
        Drive d;
        try {
            d = DriveCore.get(c);
        } catch (Exception e) {
            return waiting(c, q.size(), String.valueOf(e.getMessage()));
        }
        int total = q.size(), done = 0;
        StringBuilder dropped = new StringBuilder();
        String dest = "";
        for (File item : q) {
            File f = Outbox.content(item);
            if (f == null) { Outbox.delete(item); continue; }
            Notify.progress(c, TAG, 1, c.getString(R.string.drive_upload_title),
                    c.getString(R.string.drive_upload_progress, done + 1, total, f.getName()), done, total);
            try {
                dest = Outbox.dest(item);
                d.diskPutNew(dest, f.getName(), f.getPath());
                Outbox.delete(item);
                done++;
            } catch (Exception e) {
                String m = String.valueOf(e.getMessage());
                if ("too-big".equals(m) || "bad-name".equals(m)) {
                    if (dropped.length() > 0) dropped.append(", ");
                    dropped.append(f.getName());
                    Outbox.delete(item);
                    continue;
                }
                return waiting(c, Outbox.queued(c).size(), m);
            }
        }
        Notify.cancel(c, TAG, 1);
        String text = done > 0 ? c.getString(R.string.drive_upload_done, done, where(dest)) : "";
        if (dropped.length() > 0) {
            text += (text.isEmpty() ? "" : "\n") + c.getString(R.string.drive_upload_dropped, dropped.toString());
        }
        if (!text.isEmpty()) {
            Notify.message(c, TAG, 2, c.getString(R.string.drive_upload_title), text,
                    dropped.length() > 0 ? Notification.CATEGORY_ERROR : Notification.CATEGORY_STATUS);
        }
        return Result.success();
    }

    private Result waiting(Context c, int left, String why) {
        Notify.cancel(c, TAG, 1);
        Notify.message(c, TAG, 2, c.getString(R.string.drive_upload_title),
                c.getString(R.string.drive_upload_waiting, left, why), Notification.CATEGORY_STATUS);
        return Result.retry();
    }

    /** Sends the outbox as soon as there is a network. */
    static void start(Context c) {
        OneTimeWorkRequest req = new OneTimeWorkRequest.Builder(UploadWorker.class)
                .setConstraints(new Constraints.Builder().setRequiredNetworkType(NetworkType.CONNECTED).build())
                .setBackoffCriteria(BackoffPolicy.EXPONENTIAL, 1, TimeUnit.MINUTES)
                .build();
        WorkManager.getInstance(c).enqueueUniqueWork(WORK, ExistingWorkPolicy.APPEND_OR_REPLACE, req);
    }
}
