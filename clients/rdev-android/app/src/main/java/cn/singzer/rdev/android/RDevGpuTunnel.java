package cn.singzer.rdev.android;

import android.util.Log;

import org.json.JSONObject;

import java.io.ByteArrayOutputStream;
import java.io.IOException;
import java.net.URI;
import java.security.MessageDigest;
import java.util.HashMap;
import java.util.Locale;
import java.util.Map;

final class RDevGpuTunnel {
    private static final String TAG = "RDevTunnel";
    private static final byte FRAME_OPEN = 1;
    private static final byte FRAME_DATA = 2;
    private static final byte FRAME_CLOSE = 3;
    private final String serverUrl;
    private final String deviceId;
    private final String instanceId;
    private final String password;
    private RDevWebSocketClient ws;
    private final Map<Long, Stream> streams = new HashMap<>();

    RDevGpuTunnel(String serverUrl, String deviceId, String instanceId, String password) {
        this.serverUrl = serverUrl;
        this.deviceId = deviceId;
        this.instanceId = instanceId;
        this.password = password == null ? "" : password;
    }

    void connect() {
        String url = tunnelUrl();
        ws = new RDevWebSocketClient(url, new RDevWebSocketClient.Listener() {
            @Override public void onOpen() { Log.i(TAG, "gpu tunnel connected"); }
            @Override public void onText(String text) {}
            @Override public void onBinary(byte[] data) { handleTunnelFrame(data); }
            @Override public void onClosed(Exception error) { Log.i(TAG, "gpu tunnel closed error=" + error); }
        });
        ws.connect();
    }

    void close() {
        if (ws != null) ws.close();
        streams.clear();
    }

    private String tunnelUrl() {
        URI uri = URI.create(normalizeWsUrl(serverUrl));
        String scheme = "wss".equalsIgnoreCase(uri.getScheme()) ? "wss" : "ws";
        String authority = uri.getRawAuthority();
        StringBuilder url = new StringBuilder();
        url.append(scheme).append("://").append(authority).append("/gpu-desktop-tunnel?device=").append(urlEncode(deviceId));
        url.append("&instanceId=").append(urlEncode(instanceId));
        if (password.length() > 0) url.append("&password=").append(urlEncode(password));
        return url.toString();
    }

    private String normalizeWsUrl(String value) {
        String url = value == null ? "" : value.trim();
        if (url.startsWith("https://")) url = "wss://" + url.substring(8);
        else if (url.startsWith("http://")) url = "ws://" + url.substring(7);
        else if (!url.startsWith("ws://") && !url.startsWith("wss://")) url = "ws://" + url;
        return url;
    }

    private String urlEncode(String value) {
        try { return java.net.URLEncoder.encode(value, "UTF-8"); }
        catch (Exception e) { return value; }
    }

    private void handleTunnelFrame(byte[] data) {
        if (data.length < 9) return;
        byte frameType = data[0];
        long streamId = readU64(data, 1);
        byte[] body = slice(data, 9, data.length - 9);
        if (frameType == FRAME_OPEN) {
            streams.put(streamId, new Stream(streamId));
            Log.i(TAG, "stream open " + streamId);
        } else if (frameType == FRAME_DATA) {
            Stream stream = streams.get(streamId);
            if (stream != null) stream.onData(body);
        } else if (frameType == FRAME_CLOSE) {
            Stream stream = streams.remove(streamId);
            if (stream != null) stream.stopVideo();
            Log.i(TAG, "stream close " + streamId);
        }
    }

    private void sendFrame(byte type, long streamId, byte[] body) throws IOException {
        byte[] frame = new byte[9 + body.length];
        frame[0] = type;
        writeU64(frame, 1, streamId);
        System.arraycopy(body, 0, frame, 9, body.length);
        ws.sendBinary(frame);
    }

    private void sendData(long streamId, byte[] body) {
        try { sendFrame(FRAME_DATA, streamId, body); }
        catch (IOException e) { Log.w(TAG, "send tunnel data failed", e); }
    }

    private void sendClose(long streamId) {
        try { sendFrame(FRAME_CLOSE, streamId, new byte[0]); }
        catch (IOException ignored) {}
        streams.remove(streamId);
    }

    private long readU64(byte[] data, int off) {
        long v = 0;
        for (int i = 0; i < 8; i++) v = (v << 8) | (data[off + i] & 0xffL);
        return v;
    }

    private void writeU64(byte[] data, int off, long value) {
        for (int i = 7; i >= 0; i--) { data[off + i] = (byte) value; value >>>= 8; }
    }

    private byte[] slice(byte[] data, int off, int len) {
        byte[] out = new byte[len];
        System.arraycopy(data, off, out, 0, len);
        return out;
    }

    private final class Stream implements AndroidVideoHub.Listener {
        final long id;
        final ByteArrayOutputStream request = new ByteArrayOutputStream();
        boolean upgraded;
        boolean videoActive;
        Fmp4Muxer muxer;
        Stream(long id) { this.id = id; }

        void onData(byte[] body) {
            if (!upgraded) {
                request.write(body, 0, body.length);
                String req = request.toString();
                if (!req.contains("\r\n\r\n")) return;
                handleHttpRequest(req);
                return;
            }
            handleWebSocketBytes(body);
        }

        private void handleHttpRequest(String req) {
            String first = req.split("\r?\n", 2)[0];
            Log.i(TAG, "desktop request stream=" + id + " " + first);
            if (req.toLowerCase(Locale.US).contains("upgrade: websocket")) {
                String key = header(req, "Sec-WebSocket-Key");
                String resp = "HTTP/1.1 101 Switching Protocols\r\n" +
                    "Upgrade: websocket\r\n" +
                    "Connection: Upgrade\r\n" +
                    "Sec-WebSocket-Accept: " + websocketAccept(key) + "\r\n\r\n";
                sendData(id, ascii(resp));
                upgraded = true;
                sendInitialCapabilities(id);
            } else {
                String body = "RDev Android desktop service";
                String resp = "HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\nContent-Length: " + body.length() + "\r\n\r\n" + body;
                sendData(id, ascii(resp));
                sendClose(id);
            }
        }

        private void handleWebSocketBytes(byte[] body) {
            try {
                RDevWsFrame frame = RDevWsFrame.decode(body);
                if (frame == null || frame.opcode != 1) return;
                String text = new String(frame.payload, "UTF-8");
                Log.i(TAG, "desktop ws " + text);
                if (text.contains("GetCapturableList")) {
                    sendWsText(id, new JSONObject().put("CapturableList", new org.json.JSONArray().put("Android Screen (MediaProjection)")).toString());
                } else if (text.contains("PointerEvent")) {
                    handlePointerEvent(text);
                } else if (text.contains("Config")) {
                    sendWsText(id, "\"ConfigOk\"");
                    sendWsText(id, runtimeStatus());
                } else if (text.contains("ResumeVideo")) {
                    startVideo();
                } else if (text.contains("PauseVideo")) {
                    stopVideo();
                } else if (text.contains("InputCapabilities")) {
                    sendWsText(id, inputCapabilities());
                }
            } catch (Exception e) {
                Log.w(TAG, "desktop ws parse failed", e);
            }
        }

        private void startVideo() {
            if (videoActive) return;
            videoActive = true;
            AndroidVideoHub.addListener(this);
            Log.i(TAG, "video subscribed stream=" + id);
        }

        private void stopVideo() {
            if (!videoActive) return;
            videoActive = false;
            AndroidVideoHub.removeListener(this);
            Log.i(TAG, "video unsubscribed stream=" + id);
        }

        @Override public void onVideoConfig(int width, int height, byte[] sps, byte[] pps) {
            try {
                muxer = new Fmp4Muxer(width, height, sps, pps);
                sendWsText(id, "\"NewVideo\"");
                sendData(id, RDevWsFrame.encode(2, muxer.initSegment()));
                Log.i(TAG, "video init sent stream=" + id + " " + width + "x" + height);
            } catch (Exception e) {
                Log.w(TAG, "video init failed", e);
            }
        }

        @Override public void onVideoSample(byte[] data, long ptsUs, boolean keyFrame) {
            if (!videoActive || muxer == null) return;
            try {
                sendData(id, RDevWsFrame.encode(2, muxer.fragment(data, ptsUs, keyFrame)));
            } catch (Exception e) {
                Log.w(TAG, "video sample failed", e);
                stopVideo();
            }
        }
    }

    private void sendInitialCapabilities(long streamId) {
        try {
            sendWsText(streamId, new JSONObject().put("CapturableList", new org.json.JSONArray().put("Android Screen (MediaProjection)")).toString());
            sendWsText(streamId, encoderCapabilities());
            sendWsText(streamId, inputCapabilities());
        } catch (Exception e) {
            Log.w(TAG, "send initial desktop capabilities failed", e);
        }
    }

    private void handlePointerEvent(String text) {
        try {
            JSONObject pointer = new JSONObject(text).optJSONObject("PointerEvent");
            if (pointer == null) return;
            String eventType = pointer.optString("event_type", pointer.optString("type", ""));
            if ("pointerup".equals(eventType) || "click".equals(eventType)) {
                boolean ok = RDevAccessibilityService.tapNormalized(pointer.optDouble("x", 0.5), pointer.optDouble("y", 0.5));
                Log.i(TAG, "accessibility tap " + ok + " x=" + pointer.optDouble("x") + " y=" + pointer.optDouble("y"));
            }
        } catch (Exception e) {
            Log.w(TAG, "pointer event failed", e);
        }
    }

    private void sendWsText(long streamId, String text) {
        sendData(streamId, RDevWsFrame.encodeText(text));
    }

    private String encoderCapabilities() throws Exception {
        return new JSONObject().put("EncoderCapabilities", new JSONObject().put("options", new org.json.JSONArray()
            .put(new JSONObject().put("value", "mediacodec-h264").put("label", "MediaCodec H.264"))
            .put(new JSONObject().put("value", "auto").put("label", "自动")))).toString();
    }

    private String inputCapabilities() throws Exception {
        org.json.JSONArray options = new org.json.JSONArray()
            .put(new JSONObject().put("value", "android-accessibility").put("label", "Android Accessibility").put("kinds", new org.json.JSONArray().put("pointer").put("keyboard").put("text")))
            .put(new JSONObject().put("value", "auto").put("label", "自动"));
        return new JSONObject().put("InputCapabilities", new JSONObject()
            .put("options", options)
            .put("pointerOptions", options)
            .put("keyboardOptions", options)).toString();
    }

    private String runtimeStatus() throws Exception {
        return new JSONObject().put("RuntimeStatus", new JSONObject()
            .put("captureBackend", "android-mediaprojection")
            .put("encoderBackend", "mediacodec-h264")
            .put("inputBackend", RDevAccessibilityService.isActive() ? "android-accessibility" : "view-only")).toString();
    }

    private byte[] ascii(String value) {
        try { return value.getBytes("US-ASCII"); }
        catch (Exception e) { return value.getBytes(); }
    }

    private String header(String req, String name) {
        String prefix = name.toLowerCase(Locale.US) + ":";
        for (String line : req.split("\r?\n")) {
            if (line.toLowerCase(Locale.US).startsWith(prefix)) return line.substring(line.indexOf(':') + 1).trim();
        }
        return "";
    }

    private String websocketAccept(String key) {
        try {
            MessageDigest sha1 = MessageDigest.getInstance("SHA-1");
            byte[] digest = sha1.digest((key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11").getBytes("US-ASCII"));
            return android.util.Base64.encodeToString(digest, android.util.Base64.NO_WRAP);
        } catch (Exception e) { return ""; }
    }
}
