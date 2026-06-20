package dev.icepie.rdev;

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

import org.json.JSONArray;
import org.json.JSONObject;

import java.io.IOException;
import java.util.UUID;

public class RDevAgentService extends Service {
    public static final String ACTION_START_CAPTURE = "dev.icepie.rdev.START_CAPTURE";
    public static final String EXTRA_RESULT_CODE = "resultCode";
    public static final String EXTRA_RESULT_DATA = "resultData";
    private static final String TAG = "RDevAgent";
    private static final int NOTIFICATION_ID = 1701;
    private static final String CHANNEL_ID = "rdev-agent";

    private ScreenCapturePipeline capture;
    private RDevWebSocketClient client;
    private RDevGpuTunnel tunnel;
    private String instanceId;

    @Override public void onCreate() {
        super.onCreate();
        startForeground(NOTIFICATION_ID, notification("RDev Android 正在运行"));
        instanceId = UUID.randomUUID().toString();
    }

    @Override public int onStartCommand(Intent intent, int flags, int startId) {
        if (intent != null && ACTION_START_CAPTURE.equals(intent.getAction())) {
            startWebSocket();
            startCapture(intent.getIntExtra(EXTRA_RESULT_CODE, 0), intent.getParcelableExtra(EXTRA_RESULT_DATA));
        }
        return START_STICKY;
    }

    @Override public void onDestroy() {
        if (capture != null) {
            capture.stop();
            capture = null;
        }
        if (client != null) {
            client.close();
            client = null;
        }
        if (tunnel != null) {
            tunnel.close();
            tunnel = null;
        }
        super.onDestroy();
    }

    @Override public IBinder onBind(Intent intent) { return null; }

    private void startWebSocket() {
        SharedPreferences prefs = getSharedPreferences("rdev", MODE_PRIVATE);
        if (client != null) client.close();
        String server = prefs.getString("server", "wss://rdev.singzer.cn");
        client = new RDevWebSocketClient(server, new RDevWebSocketClient.Listener() {
            @Override public void onOpen() { sendRegister(); }
            @Override public void onText(String text) { handleText(text); }
            @Override public void onBinary(byte[] data) {}
            @Override public void onClosed(Exception error) { Log.i(TAG, "websocket closed error=" + error); }
        });
        client.connect();
    }

    private void sendRegister() {
        SharedPreferences prefs = getSharedPreferences("rdev", MODE_PRIVATE);
        try {
            JSONObject desktop = new JSONObject()
                .put("platform", "android")
                .put("displayServer", "mediaprojection")
                .put("supported", true)
                .put("viewOnly", !RDevAccessibilityService.isActive())
                .put("input", RDevAccessibilityService.isActive())
                .put("clipboard", false)
                .put("backends", new JSONArray().put("android-mediaprojection").put("gpu-desktop-tunnel"))
                .put("inputBackends", new JSONArray().put("android-accessibility"))
                .put("videoCodecs", new JSONArray().put("h264"))
                .put("encoderBackends", new JSONArray().put("mediacodec"));
            JSONObject msg = new JSONObject()
                .put("type", "register")
                .put("clientId", prefs.getString("id", "android"))
                .put("clientVersion", "android-dev")
                .put("instanceId", instanceId)
                .put("password", prefs.getString("password", ""))
                .put("desktop", desktop);
            client.sendText(msg.toString());
            Log.i(TAG, "register sent id=" + prefs.getString("id", "android"));
        } catch (Exception e) {
            Log.w(TAG, "register send failed", e);
        }
    }

    private void handleText(String text) {
        try {
            JSONObject msg = new JSONObject(text);
            String type = msg.optString("type", "");
            if ("register".equals(type)) {
                String registeredId = msg.optString("clientId", getSharedPreferences("rdev", MODE_PRIVATE).getString("id", "android"));
                Log.i(TAG, "registered as " + registeredId);
                startTunnel(registeredId);
            } else if ("desktop_start".equals(type)) {
                sendDesktopReady(msg.optString("sessionId", ""));
            } else {
                Log.d(TAG, "message type=" + type);
            }
        } catch (Exception e) {
            Log.w(TAG, "invalid websocket text: " + text, e);
        }
    }

    private void startTunnel(String registeredId) {
        SharedPreferences prefs = getSharedPreferences("rdev", MODE_PRIVATE);
        if (tunnel != null) tunnel.close();
        tunnel = new RDevGpuTunnel(
            prefs.getString("server", "wss://rdev.singzer.cn"),
            registeredId,
            instanceId,
            prefs.getString("password", "")
        );
        tunnel.connect();
    }

    private void sendDesktopReady(String sessionId) {
        if (client == null) return;
        try {
            JSONObject msg = new JSONObject()
                .put("type", "desktop_ready")
                .put("sessionId", sessionId)
                .put("width", capture != null ? capture.width() : 0)
                .put("height", capture != null ? capture.height() : 0)
                .put("format", "h264")
                .put("source", "android-mediaprojection");
            client.sendText(msg.toString());
        } catch (IOException e) {
            Log.w(TAG, "desktop_ready send failed", e);
        } catch (Exception e) {
            Log.w(TAG, "desktop_ready build failed", e);
        }
    }

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
