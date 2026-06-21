package dev.icepie.rdev;

import android.app.Notification;
import android.app.NotificationChannel;
import android.app.NotificationManager;
import android.app.PendingIntent;
import android.app.Service;
import android.content.Context;
import android.content.Intent;
import android.content.SharedPreferences;
import android.media.projection.MediaProjection;
import android.media.projection.MediaProjectionManager;
import android.net.ConnectivityManager;
import android.net.Network;
import android.net.wifi.WifiManager;
import android.os.Build;
import android.os.IBinder;
import android.os.PowerManager;
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
    private AndroidTerminalManager terminalManager;
    private AndroidFileManager fileManager;
    private PowerManager.WakeLock wakeLock;
    private WifiManager.WifiLock wifiLock;
    private ConnectivityManager.NetworkCallback networkCallback;
    private String instanceId;
    private volatile boolean stopping;
    private volatile boolean videoDemandActive;
    private volatile long suppressReconnectUntilMs;
    private int reconnectDelayMs = 1000;
    private int captureStopGeneration;

    @Override public void onCreate() {
        super.onCreate();
        startForeground(NOTIFICATION_ID, notification("RDev Android 正在运行"));
        instanceId = UUID.randomUUID().toString();
        terminalManager = new AndroidTerminalManager(this);
        fileManager = new AndroidFileManager(this);
        acquireKeepAliveLocks();
        registerNetworkCallback();
        AndroidVideoHub.setDemandListener(this::handleVideoDemandChanged);
    }

    @Override public int onStartCommand(Intent intent, int flags, int startId) {
        stopping = false;
        if (client == null) startWebSocket();
        if (intent != null && ACTION_START_CAPTURE.equals(intent.getAction())) {
            startCapture(intent.getIntExtra(EXTRA_RESULT_CODE, 0), intent.getParcelableExtra(EXTRA_RESULT_DATA));
        }
        return START_STICKY;
    }

    @Override public void onDestroy() {
        stopping = true;
        AndroidVideoHub.setDemandListener(null);
        if (capture != null) {
            capture.releaseProjection();
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
        if (terminalManager != null) terminalManager.closeAll();
        if (fileManager != null) fileManager.closeAll();
        unregisterNetworkCallback();
        releaseKeepAliveLocks();
        super.onDestroy();
    }

    @Override public IBinder onBind(Intent intent) { return null; }

    private void acquireKeepAliveLocks() {
        try {
            PowerManager powerManager = (PowerManager) getSystemService(Context.POWER_SERVICE);
            if (powerManager != null && wakeLock == null) {
                wakeLock = powerManager.newWakeLock(PowerManager.PARTIAL_WAKE_LOCK, "RDev:agent");
                wakeLock.setReferenceCounted(false);
                wakeLock.acquire();
                Log.i(TAG, "wake lock acquired");
            }
        } catch (Throwable t) {
            Log.w(TAG, "acquire wake lock failed", t);
        }
        try {
            WifiManager wifiManager = (WifiManager) getApplicationContext().getSystemService(Context.WIFI_SERVICE);
            if (wifiManager != null && wifiLock == null) {
                int mode = Build.VERSION.SDK_INT >= 12 ? WifiManager.WIFI_MODE_FULL_HIGH_PERF : WifiManager.WIFI_MODE_FULL;
                wifiLock = wifiManager.createWifiLock(mode, "RDev:agent");
                wifiLock.setReferenceCounted(false);
                wifiLock.acquire();
                Log.i(TAG, "wifi lock acquired");
            }
        } catch (Throwable t) {
            Log.w(TAG, "acquire wifi lock failed", t);
        }
    }

    private void releaseKeepAliveLocks() {
        try {
            if (wakeLock != null && wakeLock.isHeld()) wakeLock.release();
        } catch (Throwable ignored) {}
        wakeLock = null;
        try {
            if (wifiLock != null && wifiLock.isHeld()) wifiLock.release();
        } catch (Throwable ignored) {}
        wifiLock = null;
    }

    private void registerNetworkCallback() {
        try {
            ConnectivityManager manager = (ConnectivityManager) getSystemService(Context.CONNECTIVITY_SERVICE);
            if (Build.VERSION.SDK_INT < 24 || manager == null || networkCallback != null) return;
            networkCallback = new ConnectivityManager.NetworkCallback() {
                @Override public void onAvailable(Network network) {
                    if (client == null) {
                        Log.i(TAG, "network available");
                        return;
                    }
                    Log.i(TAG, "network available, reconnecting websockets");
                    reconnectNow();
                }
                @Override public void onLost(Network network) {
                    Log.i(TAG, "network lost");
                }
            };
            manager.registerDefaultNetworkCallback(networkCallback);
        } catch (Throwable t) {
            Log.w(TAG, "register network callback failed", t);
        }
    }

    private void unregisterNetworkCallback() {
        try {
            ConnectivityManager manager = (ConnectivityManager) getSystemService(Context.CONNECTIVITY_SERVICE);
            if (manager != null && networkCallback != null) manager.unregisterNetworkCallback(networkCallback);
        } catch (Throwable ignored) {}
        networkCallback = null;
    }

    private void reconnectNow() {
        if (stopping) return;
        reconnectDelayMs = 1000;
        suppressReconnectUntilMs = System.currentTimeMillis() + 5000;
        if (client != null) client.close();
        if (tunnel != null) {
            tunnel.close();
            tunnel = null;
        }
        startWebSocket();
    }

    private void startWebSocket() {
        SharedPreferences prefs = getSharedPreferences("rdev", MODE_PRIVATE);
        if (client != null) client.close();
        String server = prefs.getString("server", "wss://rdev.singzer.cn");
        client = new RDevWebSocketClient(server, new RDevWebSocketClient.Listener() {
            @Override public void onOpen() {
                reconnectDelayMs = 1000;
                sendRegister();
            }
            @Override public void onText(String text) { handleText(text); }
            @Override public void onBinary(byte[] data) { handleBinary(data); }
            @Override public void onClosed(Exception error) {
                Log.i(TAG, "websocket closed error=" + error);
                if (!stopping && System.currentTimeMillis() >= suppressReconnectUntilMs) scheduleReconnect();
            }
        });
        client.connect();
    }

    private void scheduleReconnect() {
        final int delay = reconnectDelayMs;
        reconnectDelayMs = Math.min(30000, reconnectDelayMs * 2);
        new Thread(() -> {
            try { Thread.sleep(delay); } catch (InterruptedException e) { Thread.currentThread().interrupt(); }
            if (!stopping) startWebSocket();
        }, "rdev-agent-reconnect").start();
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
            } else if ("new_session".equals(type)) {
                if (terminalManager != null) terminalManager.handleNewSession(msg);
            } else if ("stdin_close".equals(type)) {
                if (terminalManager != null) terminalManager.handleStdinClose(msg.optString("sessionId", ""));
            } else if ("resize".equals(type)) {
                if (terminalManager != null) terminalManager.handleResize(msg.optString("sessionId", ""), msg.optInt("rows", 0), msg.optInt("cols", 0));
            } else if ("close".equals(type)) {
                String sessionId = msg.optString("sessionId", "");
                if (terminalManager != null) terminalManager.close(sessionId);
                String taskId = msg.optString("taskId", "");
                if (fileManager != null && taskId.length() > 0) fileManager.cancel(taskId);
            } else if ("file_list".equals(type)) {
                if (fileManager != null) new Thread(() -> fileManager.handleList(msg), "rdev-file-list").start();
            } else if ("file_upload_start".equals(type)) {
                if (fileManager != null) fileManager.handleUploadStart(msg);
            } else if ("file_upload_end".equals(type)) {
                if (fileManager != null) fileManager.handleUploadEnd(msg);
            } else if ("file_download_start".equals(type)) {
                if (fileManager != null) fileManager.handleDownloadStart(msg);
            } else if ("file_stat".equals(type)) {
                if (fileManager != null) fileManager.handleStat(msg);
            } else if ("file_mkdir".equals(type)) {
                if (fileManager != null) fileManager.handleMkdir(msg);
            } else if ("file_delete".equals(type)) {
                if (fileManager != null) fileManager.handleDelete(msg);
            } else if ("file_rename".equals(type) || "file_move".equals(type)) {
                if (fileManager != null) fileManager.handleRename(msg);
            } else if ("file_copy".equals(type)) {
                if (fileManager != null) fileManager.handleCopy(msg);
            } else if ("file_transfer_cancel".equals(type)) {
                if (fileManager != null) fileManager.cancel(msg.optString("taskId", ""));
            } else {
                Log.d(TAG, "message type=" + type);
            }
        } catch (Exception e) {
            Log.w(TAG, "invalid websocket text: " + text, e);
        }
    }

    private void handleBinary(byte[] data) {
        try {
            RDevProtocol frame = RDevProtocol.decode(data);
            if (frame.type == RDevProtocol.BIN_DATA) {
                if (terminalManager != null) terminalManager.handleInput(frame.id, frame.payload);
            } else if (frame.type == RDevProtocol.BIN_FILE_UPLOAD_CHUNK) {
                if (fileManager != null) fileManager.handleUploadChunk(frame.id, frame.offset, frame.offsetPayload);
            } else if (frame.type == RDevProtocol.BIN_FILE_TRANSFER_CANCEL) {
                if (fileManager != null) fileManager.cancel(frame.id);
            }
        } catch (Exception e) {
            Log.w(TAG, "invalid websocket binary", e);
        }
    }

    void sendText(JSONObject msg) {
        if (client == null) return;
        try {
            client.sendText(msg.toString());
        } catch (IOException e) {
            Log.w(TAG, "send text failed", e);
        }
    }

    void sendBinary(int type, String id, byte[] data, int len) {
        if (client == null) return;
        try {
            client.sendBinary(RDevProtocol.encode(type, id, data, len));
        } catch (Exception e) {
            Log.w(TAG, "send binary failed type=" + type + " id=" + id, e);
        }
    }

    boolean sendBinaryOffset(int type, String id, long offset, byte[] data) {
        return sendBinaryOffset(type, id, offset, data, data == null ? 0 : data.length);
    }

    boolean sendBinaryOffset(int type, String id, long offset, byte[] data, int len) {
        if (client == null) return false;
        try {
            client.sendBinary(RDevProtocol.encodeOffset(type, id, offset, data == null ? new byte[0] : data, len));
            return true;
        } catch (Exception e) {
            Log.w(TAG, "send binary offset failed type=" + type + " id=" + id, e);
            return false;
        }
    }

    void sendExitCode(String sessionId, int code) {
        try {
            sendText(new JSONObject().put("type", "exit_code").put("sessionId", sessionId).put("exitCode", code));
        } catch (Exception e) {
            Log.w(TAG, "exit_code build failed", e);
        }
    }

    void sendError(String sessionId, String error) {
        try {
            sendText(new JSONObject().put("type", "error").put("sessionId", sessionId).put("error", error == null ? "unknown error" : error));
        } catch (Exception e) {
            Log.w(TAG, "error build failed", e);
        }
    }

    void sendClose(String sessionId) {
        try {
            sendText(new JSONObject().put("type", "close").put("sessionId", sessionId));
        } catch (Exception e) {
            Log.w(TAG, "close build failed", e);
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

    private void handleVideoDemandChanged(boolean active) {
        videoDemandActive = active;
        if (active) {
            captureStopGeneration++;
            startCaptureIfNeeded();
            return;
        }
        scheduleCapturePause();
    }

    private synchronized void startCaptureIfNeeded() {
        if (capture == null) {
            Log.i(TAG, "video viewer active but MediaProjection is not granted");
            return;
        }
        if (!capture.isRunning()) {
            Log.i(TAG, "video viewer active, starting capture");
            capture.start();
        }
    }

    private void scheduleCapturePause() {
        final int generation = ++captureStopGeneration;
        new Thread(() -> {
            try { Thread.sleep(3000); } catch (InterruptedException e) { Thread.currentThread().interrupt(); }
            if (stopping || videoDemandActive || generation != captureStopGeneration) return;
            ScreenCapturePipeline current = capture;
            if (current != null && current.isRunning()) {
                Log.i(TAG, "no video viewers, pausing capture");
                current.stop();
            }
        }, "rdev-capture-idle-stop").start();
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
        if (capture != null) capture.releaseProjection();
        capture = new ScreenCapturePipeline(this, projection);
        if (AndroidVideoHub.hasListeners()) {
            startCaptureIfNeeded();
        } else {
            Log.i(TAG, "MediaProjection grant stored; capture will start when viewer subscribes");
        }
    }

    private Notification notification(String text) {
        NotificationManager manager = (NotificationManager) getSystemService(Context.NOTIFICATION_SERVICE);
        PendingIntent pendingIntent = PendingIntent.getActivity(
            this,
            0,
            new Intent(this, MainActivity.class).addFlags(Intent.FLAG_ACTIVITY_SINGLE_TOP | Intent.FLAG_ACTIVITY_CLEAR_TOP),
            Build.VERSION.SDK_INT >= 23 ? PendingIntent.FLAG_UPDATE_CURRENT | PendingIntent.FLAG_IMMUTABLE : PendingIntent.FLAG_UPDATE_CURRENT
        );
        if (Build.VERSION.SDK_INT >= 26) {
            NotificationChannel channel = new NotificationChannel(CHANNEL_ID, "RDev Agent", NotificationManager.IMPORTANCE_LOW);
            manager.createNotificationChannel(channel);
            return new Notification.Builder(this, CHANNEL_ID)
                .setContentTitle("RDev Android")
                .setContentText(text)
                .setSmallIcon(android.R.drawable.stat_sys_upload)
                .setContentIntent(pendingIntent)
                .setOngoing(true)
                .build();
        }
        return new Notification.Builder(this)
            .setContentTitle("RDev Android")
            .setContentText(text)
            .setSmallIcon(android.R.drawable.stat_sys_upload)
            .setContentIntent(pendingIntent)
            .setOngoing(true)
            .build();
    }
}
