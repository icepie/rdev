package cn.singzer.rdev.android;

import android.app.Notification;
import android.app.NotificationChannel;
import android.app.NotificationManager;
import android.app.Service;
import android.content.Context;
import android.content.Intent;
import android.content.SharedPreferences;
import android.media.projection.MediaProjection;
import android.media.projection.MediaProjectionManager;
import android.os.Build;
import android.os.IBinder;
import android.util.Log;

public class RDevAgentService extends Service {
    public static final String ACTION_START_CAPTURE = "cn.singzer.rdev.android.START_CAPTURE";
    public static final String EXTRA_RESULT_CODE = "resultCode";
    public static final String EXTRA_RESULT_DATA = "resultData";
    private static final String TAG = "RDevAgent";
    private static final int NOTIFICATION_ID = 1701;
    private static final String CHANNEL_ID = "rdev-agent";

    private ScreenCapturePipeline capture;

    @Override public void onCreate() {
        super.onCreate();
        startForeground(NOTIFICATION_ID, notification("RDev Android 正在运行"));
    }

    @Override public int onStartCommand(Intent intent, int flags, int startId) {
        if (intent != null && ACTION_START_CAPTURE.equals(intent.getAction())) {
            startCapture(intent.getIntExtra(EXTRA_RESULT_CODE, 0), intent.getParcelableExtra(EXTRA_RESULT_DATA));
        }
        return START_STICKY;
    }

    @Override public void onDestroy() {
        if (capture != null) {
            capture.stop();
            capture = null;
        }
        super.onDestroy();
    }

    @Override public IBinder onBind(Intent intent) { return null; }

    private void startCapture(int resultCode, Intent resultData) {
        SharedPreferences prefs = getSharedPreferences("rdev", MODE_PRIVATE);
        Log.i(TAG, "start capture server=" + prefs.getString("server", "") + " id=" + prefs.getString("id", ""));
        if (resultCode == 0 || resultData == null) {
            Log.w(TAG, "missing MediaProjection grant");
            return;
        }
        MediaProjectionManager manager = (MediaProjectionManager) getSystemService(Context.MEDIA_PROJECTION_SERVICE);
        MediaProjection projection = manager.getMediaProjection(resultCode, resultData);
        if (projection == null) {
            Log.w(TAG, "MediaProjection is null");
            return;
        }
        if (capture != null) capture.stop();
        capture = new ScreenCapturePipeline(this, projection);
        capture.start();
    }

    private Notification notification(String text) {
        NotificationManager manager = (NotificationManager) getSystemService(Context.NOTIFICATION_SERVICE);
        if (Build.VERSION.SDK_INT >= 26) {
            NotificationChannel channel = new NotificationChannel(CHANNEL_ID, "RDev Agent", NotificationManager.IMPORTANCE_LOW);
            manager.createNotificationChannel(channel);
            return new Notification.Builder(this, CHANNEL_ID)
                .setContentTitle("RDev Android")
                .setContentText(text)
                .setSmallIcon(android.R.drawable.stat_sys_upload)
                .build();
        }
        return new Notification.Builder(this)
            .setContentTitle("RDev Android")
            .setContentText(text)
            .setSmallIcon(android.R.drawable.stat_sys_upload)
            .build();
    }
}
