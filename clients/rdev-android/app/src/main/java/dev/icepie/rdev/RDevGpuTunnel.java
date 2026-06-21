package dev.icepie.rdev;

import android.util.Log;

import org.json.JSONObject;

import java.io.ByteArrayOutputStream;
import java.io.IOException;
import java.net.URI;
import java.security.MessageDigest;
import java.util.ArrayList;
import java.util.HashMap;
import java.util.List;
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
    private volatile boolean closed;
    private int reconnectDelayMs = 1000;
    private final Map<Long, Stream> streams = new HashMap<>();
    private final Map<Integer, PointerTrace> activePointers = new HashMap<>();
    private final List<PointerTrace> completedPointers = new ArrayList<>();
    private boolean inputPermissionWarned;

    RDevGpuTunnel(String serverUrl, String deviceId, String instanceId, String password) {
        this.serverUrl = serverUrl;
        this.deviceId = deviceId;
        this.instanceId = instanceId;
        this.password = password == null ? "" : password;
    }

    void connect() {
        closed = false;
        connectOnce();
    }

    private void connectOnce() {
        String url = tunnelUrl();
        ws = new RDevWebSocketClient(url, new RDevWebSocketClient.Listener() {
            @Override public void onOpen() {
                reconnectDelayMs = 1000;
                Log.i(TAG, "gpu tunnel connected");
            }
            @Override public void onText(String text) {}
            @Override public void onBinary(byte[] data) { handleTunnelFrame(data); }
            @Override public void onClosed(Exception error) {
                Log.i(TAG, "gpu tunnel closed error=" + error);
                clearStreams();
                if (!closed) scheduleReconnect();
            }
        });
        ws.connect();
    }

    void close() {
        closed = true;
        if (ws != null) ws.close();
        clearStreams();
    }

    private void scheduleReconnect() {
        final int delay = reconnectDelayMs;
        reconnectDelayMs = Math.min(30000, reconnectDelayMs * 2);
        new Thread(() -> {
            try { Thread.sleep(delay); } catch (InterruptedException e) { Thread.currentThread().interrupt(); }
            if (!closed) connectOnce();
        }, "rdev-tunnel-reconnect").start();
    }

    private void clearStreams() {
        for (Stream stream : streams.values()) stream.stopVideo();
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
        boolean waitingForKeyFrame;
        byte[] sps;
        byte[] pps;
        Fmp4Muxer muxer;
        long lastVideoSentUs;
        long lastBatchFlushMs;
        ByteArrayOutputStream videoBatch = new ByteArrayOutputStream(96 * 1024);
        int videoBatchFrames;
        int targetFps = 15;
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
                } else if (text.contains("PointerEvent") || text.contains("\"op\":\"input\"")) {
                    handlePointerEvent(text);
                } else if (text.contains("WheelEvent")) {
                    handleWheelEvent(text);
                } else if (text.contains("TextInputEvent") || text.contains("KeyboardEvent")) {
                    handleKeyboardEvent(text);
                } else if (text.contains("Config")) {
                    updateConfig(text);
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

        private void updateConfig(String text) {
            try {
                JSONObject config = new JSONObject(text).optJSONObject("Config");
                if (config == null) return;
                int requested = config.optInt("frame_rate", targetFps);
                if (requested > 0) targetFps = Math.max(8, Math.min(15, requested));
                Log.i(TAG, "desktop config targetFps=" + targetFps + " encoder=" + config.optString("encoder", "auto"));
            } catch (Exception e) {
                Log.w(TAG, "config parse failed", e);
            }
        }

        private void startVideo() {
            if (videoActive) return;
            videoActive = true;
            waitingForKeyFrame = true;
            lastVideoSentUs = 0;
            AndroidVideoHub.addListener(this);
            AndroidVideoHub.requestKeyFrame();
            Log.i(TAG, "video subscribed stream=" + id + " targetFps=" + targetFps);
        }

        private void stopVideo() {
            if (!videoActive) return;
            videoActive = false;
            AndroidVideoHub.removeListener(this);
            flushVideoBatch();
            Log.i(TAG, "video unsubscribed stream=" + id);
        }

        @Override public void onVideoConfig(int width, int height, byte[] sps, byte[] pps) {
            try {
                muxer = null;
                this.sps = ensureStartCode(sps);
                this.pps = ensureStartCode(pps);
                videoBatch.reset();
                videoBatchFrames = 0;
                waitingForKeyFrame = true;
                lastBatchFlushMs = 0;
                sendWsText(id, androidVideoConfig(width, height, sps));
                AndroidVideoHub.requestKeyFrame();
                Log.i(TAG, "android video config sent stream=" + id + " " + width + "x" + height);
            } catch (Exception e) {
                Log.w(TAG, "video init failed", e);
            }
        }

        @Override public void onVideoSample(byte[] data, long ptsUs, boolean keyFrame) {
            if (!videoActive) return;
            if (!keyFrame) {
                AndroidVideoHub.requestKeyFrame();
                return;
            }
            if (waitingForKeyFrame && !keyFrame) return;
            long minIntervalUs = 1_000_000L / Math.max(1, targetFps);
            if (lastVideoSentUs > 0 && ptsUs - lastVideoSentUs < minIntervalUs) return;
            try {
                byte[] sample = keyFrame ? prependParameterSets(data, sps, pps) : toAnnexB(data);
                sendData(id, RDevWsFrame.encode(2, androidVideoPacket(sample, ptsUs, keyFrame)));
                waitingForKeyFrame = false;
                lastVideoSentUs = ptsUs;
            } catch (Exception e) {
                Log.w(TAG, "video sample failed", e);
                stopVideo();
            }
        }

        private void flushVideoBatch() {
            if (videoBatchFrames == 0 || videoBatch.size() == 0) return;
            sendData(id, RDevWsFrame.encode(2, videoBatch.toByteArray()));
            videoBatch.reset();
            videoBatchFrames = 0;
            lastBatchFlushMs = System.currentTimeMillis();
        }
    }

    private String androidVideoConfig(int width, int height, byte[] sps) throws Exception {
        return new JSONObject().put("AndroidVideoConfig", new JSONObject()
            .put("codec", avcCodecString(sps))
            .put("width", width)
            .put("height", height)
            .put("format", "annexb")
            .put("transport", "webcodecs-h264"))
            .toString();
    }

    private String avcCodecString(byte[] sps) {
        byte[] clean = stripStartCode(sps);
        if (clean.length >= 4) {
            return String.format(java.util.Locale.US, "avc1.%02X%02X%02X", clean[1] & 0xff, clean[2] & 0xff, clean[3] & 0xff);
        }
        return "avc1.42E01F";
    }

    private byte[] androidVideoPacket(byte[] sample, long ptsUs, boolean keyFrame) {
        byte[] packet = new byte[13 + sample.length];
        packet[0] = 'R'; packet[1] = 'D'; packet[2] = 'A'; packet[3] = '1';
        packet[4] = (byte) (keyFrame ? 1 : 0);
        for (int i = 0; i < 8; i++) packet[5 + i] = (byte) (ptsUs >>> (56 - i * 8));
        System.arraycopy(sample, 0, packet, 13, sample.length);
        return packet;
    }

    private byte[] stripStartCode(byte[] data) {
        if (data == null) return new byte[0];
        int off = 0;
        if (data.length >= 4 && data[0] == 0 && data[1] == 0 && data[2] == 0 && data[3] == 1) off = 4;
        else if (data.length >= 3 && data[0] == 0 && data[1] == 0 && data[2] == 1) off = 3;
        byte[] out = new byte[data.length - off];
        System.arraycopy(data, off, out, 0, out.length);
        return out;
    }

    private byte[] ensureStartCode(byte[] data) {
        if (data == null || data.length == 0) return new byte[0];
        if (hasStartCode(data)) return data;
        byte[] out = new byte[data.length + 4];
        out[0] = 0; out[1] = 0; out[2] = 0; out[3] = 1;
        System.arraycopy(data, 0, out, 4, data.length);
        return out;
    }

    private boolean hasStartCode(byte[] data) {
        return data.length >= 4 && data[0] == 0 && data[1] == 0 && data[2] == 0 && data[3] == 1
            || data.length >= 3 && data[0] == 0 && data[1] == 0 && data[2] == 1;
    }

    private byte[] toAnnexB(byte[] sample) {
        if (sample == null || sample.length == 0) return new byte[0];
        if (hasStartCode(sample)) return sample;
        byte[] converted = convertLengthPrefixedNal(sample, 4);
        if (converted.length > 0) return converted;
        converted = convertLengthPrefixedNal(sample, 2);
        if (converted.length > 0) return converted;
        return ensureStartCode(sample);
    }

    private byte[] convertLengthPrefixedNal(byte[] sample, int lengthSize) {
        try {
            ByteArrayOutputStream out = new ByteArrayOutputStream(sample.length + 32);
            int pos = 0;
            int count = 0;
            while (pos + lengthSize <= sample.length) {
                int nalSize = 0;
                for (int i = 0; i < lengthSize; i++) nalSize = (nalSize << 8) | (sample[pos + i] & 0xff);
                pos += lengthSize;
                if (nalSize <= 0 || pos + nalSize > sample.length) return new byte[0];
                out.write(0); out.write(0); out.write(0); out.write(1);
                out.write(sample, pos, nalSize);
                pos += nalSize;
                count++;
            }
            return pos == sample.length && count > 0 ? out.toByteArray() : new byte[0];
        } catch (Exception e) {
            return new byte[0];
        }
    }

    private byte[] prependParameterSets(byte[] sample, byte[] sps, byte[] pps) {
        byte[] frame = toAnnexB(sample);
        int spsLen = sps == null ? 0 : sps.length;
        int ppsLen = pps == null ? 0 : pps.length;
        byte[] out = new byte[spsLen + ppsLen + frame.length];
        int off = 0;
        if (spsLen > 0) { System.arraycopy(sps, 0, out, off, spsLen); off += spsLen; }
        if (ppsLen > 0) { System.arraycopy(pps, 0, out, off, ppsLen); off += ppsLen; }
        System.arraycopy(frame, 0, out, off, frame.length);
        return out;
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
        if (!inputReady()) return;
        try {
            JSONObject root = new JSONObject(text);
            JSONObject pointer = root.optJSONObject("PointerEvent");
            String eventType;
            double x;
            double y;
            if (pointer != null) {
                eventType = pointer.optString("event_type", pointer.optString("type", ""));
                x = pointer.optDouble("x", 0.5);
                y = pointer.optDouble("y", 0.5);
            } else if ("input".equals(root.optString("op"))) {
                eventType = root.optString("inputType", "");
                x = root.optDouble("x", 160) / 320.0;
                y = root.optDouble("y", 320) / 640.0;
            } else {
                return;
            }
            int pointerId = pointer != null ? pointer.optInt("pointer_id", 0) : 0;
            if ("pointerdown".equals(eventType) || "mouse_down".equals(eventType)) {
                synchronized (activePointers) {
                    activePointers.put(pointerId, new PointerTrace(x, y, System.currentTimeMillis()));
                }
            } else if ("pointermove".equals(eventType) || "mouse_move".equals(eventType)) {
                synchronized (activePointers) {
                    PointerTrace trace = activePointers.get(pointerId);
                    if (trace != null) trace.update(x, y);
                }
            } else if ("pointerup".equals(eventType) || "click".equals(eventType) || "mouse_up".equals(eventType)) {
                handlePointerUp(pointerId, x, y);
            } else if ("pointercancel".equals(eventType)) {
                synchronized (activePointers) { activePointers.remove(pointerId); }
            }
        } catch (Exception e) {
            Log.w(TAG, "pointer event failed", e);
        }
    }

    private void handlePointerUp(int pointerId, double x, double y) {
        PointerTrace trace;
        synchronized (activePointers) {
            trace = activePointers.remove(pointerId);
            if (trace == null) trace = new PointerTrace(x, y, System.currentTimeMillis());
            trace.update(x, y);
            completedPointers.add(trace);
            if (!activePointers.isEmpty()) return;
            List<PointerTrace> traces = new ArrayList<>(completedPointers);
            completedPointers.clear();
            dispatchPointerTraces(traces);
        }
    }

    private void dispatchPointerTraces(List<PointerTrace> traces) {
        if (traces.isEmpty()) return;
        if (traces.size() == 1) {
            PointerTrace trace = traces.get(0);
            if (trace.distance() < 0.015) {
                boolean ok = RDevAccessibilityService.tapNormalized(trace.endX, trace.endY);
                Log.i(TAG, "accessibility tap " + ok + " x=" + trace.endX + " y=" + trace.endY);
            } else {
                boolean ok = RDevAccessibilityService.swipeNormalized(trace.startX, trace.startY, trace.endX, trace.endY, trace.durationMs());
                Log.i(TAG, "accessibility swipe " + ok + " from=" + trace.startX + "," + trace.startY + " to=" + trace.endX + "," + trace.endY);
            }
            return;
        }
        double[] startX = new double[traces.size()];
        double[] startY = new double[traces.size()];
        double[] endX = new double[traces.size()];
        double[] endY = new double[traces.size()];
        long duration = 80;
        for (int i = 0; i < traces.size(); i++) {
            PointerTrace trace = traces.get(i);
            startX[i] = trace.startX;
            startY[i] = trace.startY;
            endX[i] = trace.endX;
            endY[i] = trace.endY;
            duration = Math.max(duration, trace.durationMs());
        }
        boolean ok = RDevAccessibilityService.multiSwipeNormalized(startX, startY, endX, endY, duration);
        Log.i(TAG, "accessibility multi touch " + ok + " count=" + traces.size());
    }

    private void handleWheelEvent(String text) {
        if (!inputReady()) return;
        try {
            JSONObject wheel = new JSONObject(text).optJSONObject("WheelEvent");
            if (wheel == null) return;
            double dy = wheel.optDouble("dy", 0);
            if (Math.abs(dy) < 1) return;
            double amount = Math.max(-0.35, Math.min(0.35, dy / 1200.0));
            boolean ok = RDevAccessibilityService.swipeNormalized(0.5, 0.5, 0.5, 0.5 - amount, 140);
            Log.i(TAG, "accessibility wheel swipe " + ok + " dy=" + dy);
        } catch (Exception e) {
            Log.w(TAG, "wheel event failed", e);
        }
    }

    private void handleKeyboardEvent(String text) {
        try {
            JSONObject root = new JSONObject(text);
            JSONObject textInput = root.optJSONObject("TextInputEvent");
            if (textInput != null) {
                String value = textInput.optString("text", "");
                boolean ok = RDevInputMethodService.commitText(value);
                if (!ok) ok = RDevAccessibilityService.inputText(value);
                Log.i(TAG, "keyboard text " + ok + " ime=" + RDevInputMethodService.isActive());
                return;
            }
            JSONObject keyboard = root.optJSONObject("KeyboardEvent");
            if (keyboard == null || !"down".equals(keyboard.optString("event_type", ""))) return;
            String key = keyboard.optString("key", "");
            boolean ok = handleKeyboardKey(key);
            Log.i(TAG, "keyboard key " + ok + " key=" + key + " ime=" + RDevInputMethodService.isActive());
        } catch (Exception e) {
            Log.w(TAG, "keyboard event failed", e);
        }
    }

    private static final class PointerTrace {
        final double startX;
        final double startY;
        final long startMs;
        double endX;
        double endY;
        long endMs;

        PointerTrace(double x, double y, long now) {
            startX = x;
            startY = y;
            endX = x;
            endY = y;
            startMs = now;
            endMs = now;
        }

        void update(double x, double y) {
            endX = x;
            endY = y;
            endMs = System.currentTimeMillis();
        }

        double distance() {
            double dx = endX - startX;
            double dy = endY - startY;
            return Math.sqrt(dx * dx + dy * dy);
        }

        long durationMs() {
            return Math.max(80, endMs - startMs);
        }
    }

    private void sendWsText(long streamId, String text) {
        sendData(streamId, RDevWsFrame.encodeText(text));
    }

    private boolean handleKeyboardKey(String key) {
        if ("Backspace".equals(key)) return RDevInputMethodService.deleteBackward() || RDevAccessibilityService.backspace();
        if ("Enter".equals(key)) return RDevInputMethodService.sendKey(android.view.KeyEvent.KEYCODE_ENTER) || RDevInputMethodService.commitText("\n");
        if ("Tab".equals(key)) return RDevInputMethodService.sendKey(android.view.KeyEvent.KEYCODE_TAB) || RDevInputMethodService.commitText("\t");
        if ("ArrowLeft".equals(key)) return RDevInputMethodService.sendKey(android.view.KeyEvent.KEYCODE_DPAD_LEFT);
        if ("ArrowRight".equals(key)) return RDevInputMethodService.sendKey(android.view.KeyEvent.KEYCODE_DPAD_RIGHT);
        if ("ArrowUp".equals(key)) return RDevInputMethodService.sendKey(android.view.KeyEvent.KEYCODE_DPAD_UP);
        if ("ArrowDown".equals(key)) return RDevInputMethodService.sendKey(android.view.KeyEvent.KEYCODE_DPAD_DOWN);
        if ("Home".equals(key)) return RDevInputMethodService.sendKey(android.view.KeyEvent.KEYCODE_MOVE_HOME);
        if ("End".equals(key)) return RDevInputMethodService.sendKey(android.view.KeyEvent.KEYCODE_MOVE_END);
        if ("Escape".equals(key)) return RDevAccessibilityService.globalBack();
        if (key != null && key.length() == 1) return RDevInputMethodService.commitText(key) || RDevAccessibilityService.inputText(key);
        return false;
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
            .put("inputBackend", RDevAccessibilityService.isActive() ? "android-accessibility" : "view-only")
            .put("keyboardBackend", RDevInputMethodService.isActive() ? "android-ime" : (RDevAccessibilityService.isActive() ? "android-accessibility" : "view-only"))
            .put("inputReady", RDevAccessibilityService.isActive())
            .put("keyboardReady", RDevInputMethodService.isActive())).toString();
    }

    private boolean inputReady() {
        if (RDevAccessibilityService.isActive()) return true;
        if (!inputPermissionWarned) {
            inputPermissionWarned = true;
            Log.w(TAG, "remote pointer input ignored: enable Android Accessibility service RDev Remote Input");
        }
        return false;
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
