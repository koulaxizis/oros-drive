package online.useoros.drive.app;

import android.content.Context;
import android.content.SharedPreferences;

import androidx.annotation.NonNull;
import androidx.work.Constraints;
import androidx.work.ExistingPeriodicWorkPolicy;
import androidx.work.ExistingWorkPolicy;
import androidx.work.NetworkType;
import androidx.work.OneTimeWorkRequest;
import androidx.work.PeriodicWorkRequest;
import androidx.work.WorkManager;
import androidx.work.Worker;
import androidx.work.WorkerParameters;

import java.util.concurrent.TimeUnit;

import online.useoros.drive.mobile.Drive;

/**
 * orOS Drive in the background (DRIVE-A): one mirror pass every 15
 * minutes (Android's shortest period), on Wi-Fi only and/or while
 * charging when the person chose so, then the offline files. A pass
 * costs one listing of each folder; unchanged files are not read again.
 */
public class DriveWorker extends Worker {
    static final String PREFS = "oros-drive";
    private static final String PERIODIC = "oros-drive-periodic";
    private static final String NOW = "oros-drive-now";

    public DriveWorker(@NonNull Context c, @NonNull WorkerParameters p) { super(c, p); }

    @NonNull @Override
    public Result doWork() {
        try {
            Drive d = DriveCore.get(getApplicationContext());
            try {
                for (int i = 0; i < 3; i++) {
                    if (!d.sync()) break;      // files still being written: once more, a bit later
                    Thread.sleep(5000);
                }
            } finally {
                // Offline files (wave 3): what is pinned and not here yet,
                // under the same Wi-Fi / charging conditions.
                try { d.diskSyncPins(); } catch (Exception ignored) { }
            }
            return Result.success();
        } catch (Exception e) {
            // Locked, signed out or offline: the screen shows why; the
            // next period tries again.
            return Result.success();
        }
    }

    private static Constraints constraints(Context c) {
        SharedPreferences p = c.getSharedPreferences(PREFS, Context.MODE_PRIVATE);
        return new Constraints.Builder()
                .setRequiredNetworkType(p.getBoolean("wifi-only", true) ? NetworkType.UNMETERED : NetworkType.CONNECTED)
                .setRequiresCharging(p.getBoolean("charging-only", false))
                .setRequiresBatteryNotLow(true)
                .build();
    }

    /** (Re)schedules the periodic pass with the current options. */
    static void schedule(Context c) {
        PeriodicWorkRequest req = new PeriodicWorkRequest.Builder(DriveWorker.class, 15, TimeUnit.MINUTES)
                .setConstraints(constraints(c))
                .build();
        WorkManager.getInstance(c).enqueueUniquePeriodicWork(PERIODIC, ExistingPeriodicWorkPolicy.UPDATE, req);
    }

    /** One pass as soon as the network allows ("Sync now"). */
    static void now(Context c) {
        OneTimeWorkRequest req = new OneTimeWorkRequest.Builder(DriveWorker.class)
                .setConstraints(new Constraints.Builder().setRequiredNetworkType(NetworkType.CONNECTED).build())
                .build();
        WorkManager.getInstance(c).enqueueUniqueWork(NOW, ExistingWorkPolicy.REPLACE, req);
    }

    static void cancel(Context c) {
        WorkManager.getInstance(c).cancelUniqueWork(PERIODIC);
    }
}
