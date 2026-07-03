package dev.icepie.rdev;

import java.io.IOException;
import java.net.URI;
import java.util.Locale;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.atomic.AtomicBoolean;

import okhttp3.OkHttpClient;
import okhttp3.Request;
import okhttp3.Response;
import okhttp3.WebSocket;
import okhttp3.WebSocketListener;
import okio.ByteString;

final class RDevWebSocketClient implements RDevControlConnection {
    interface Listener {
        void onOpen();
        void onText(String text);
        void onBinary(byte[] data);
        void onClosed(Exception error);
    }

    private final String rawUrl;
    private final Listener listener;
    private final OkHttpClient client;
    private final AtomicBoolean closedNotified = new AtomicBoolean(false);
    private volatile boolean running;
    private volatile WebSocket socket;

    RDevWebSocketClient(String rawUrl, Listener listener) {
        this.rawUrl = normalizeUrl(selectWebSocketEndpoint(rawUrl));
        this.listener = listener;
        this.client = new OkHttpClient.Builder()
            .pingInterval(25, TimeUnit.SECONDS)
            .connectTimeout(15, TimeUnit.SECONDS)
            .readTimeout(0, TimeUnit.MILLISECONDS)
            .retryOnConnectionFailure(true)
            .build();
    }

    RDevWebSocketClient(String rawUrl, RDevControlConnection.Listener listener) {
        this(rawUrl, new Listener() {
            @Override public void onOpen() { listener.onOpen(); }
            @Override public void onText(String text) { listener.onText(text); }
            @Override public void onBinary(byte[] data) { listener.onBinary(data); }
            @Override public void onClosed(Exception error) { listener.onClosed(error); }
        });
    }

    @Override public void connect() {
        running = true;
        closedNotified.set(false);
        Request request = new Request.Builder()
            .url(rawUrl)
            .header("User-Agent", "rdev-android/0.1")
            .build();
        socket = client.newWebSocket(request, new WebSocketListener() {
            @Override public void onOpen(WebSocket webSocket, Response response) {
                socket = webSocket;
                listener.onOpen();
            }

            @Override public void onMessage(WebSocket webSocket, String text) {
                listener.onText(text);
            }

            @Override public void onMessage(WebSocket webSocket, ByteString bytes) {
                listener.onBinary(bytes.toByteArray());
            }

            @Override public void onClosing(WebSocket webSocket, int code, String reason) {
                running = false;
                webSocket.close(code, reason);
            }

            @Override public void onClosed(WebSocket webSocket, int code, String reason) {
                running = false;
                notifyClosed(null);
            }

            @Override public void onFailure(WebSocket webSocket, Throwable t, Response response) {
                running = false;
                notifyClosed(t instanceof Exception ? (Exception) t : new IOException(t));
            }
        });
    }

    @Override public void close() {
        running = false;
        WebSocket ws = socket;
        socket = null;
        if (ws != null) ws.close(1000, "closed");
        client.dispatcher().executorService().shutdown();
    }

    @Override public void sendText(String text) throws IOException {
        WebSocket ws = socket;
        if (!running || ws == null || !ws.send(text)) throw new IOException("websocket not connected");
    }

    @Override public void sendBinary(byte[] data) throws IOException {
        WebSocket ws = socket;
        if (!running || ws == null || !ws.send(ByteString.of(data))) throw new IOException("websocket not connected");
    }

    private void notifyClosed(Exception error) {
        if (closedNotified.compareAndSet(false, true)) listener.onClosed(error);
    }

    private static String normalizeUrl(String value) {
        String url = value == null ? "" : value.trim();
        if (url.startsWith("https://")) url = "wss://" + url.substring(8);
        else if (url.startsWith("http://")) url = "ws://" + url.substring(7);
        else if (!url.startsWith("ws://") && !url.startsWith("wss://")) url = "ws://" + url;
        try {
            URI uri = URI.create(url);
            String path = uri.getRawPath();
            String query = uri.getRawQuery();
            if (path == null || path.length() == 0 || "/".equals(path)) path = "/ws";
            else if (!path.endsWith("/ws")) path = path.endsWith("/") ? path + "ws" : path + "/ws";
            URI rebuilt = new URI(uri.getScheme(), uri.getUserInfo(), uri.getHost(), uri.getPort(), path, query, uri.getFragment());
            return rebuilt.toString();
        } catch (Throwable ignored) {
            if (!url.endsWith("/ws")) url += "/ws";
            return url;
        }
    }

    static String selectWebSocketEndpoint(String raw) {
        String value = raw == null ? "" : raw.trim();
        if (value.indexOf(',') < 0) return value;
        String first = "";
        for (String part : value.split(",")) {
            String endpoint = part.trim();
            if (endpoint.length() == 0) continue;
            if (first.length() == 0) first = endpoint;
            String lower = endpoint.toLowerCase(Locale.US);
            if (lower.startsWith("ws://") || lower.startsWith("wss://") || lower.startsWith("http://") || lower.startsWith("https://")) return endpoint;
        }
        if (first.length() == 0) return value;
        return deriveWsFromEndpoint(first);
    }

    static String deriveWsFromEndpoint(String endpoint) {
        String value = endpoint == null ? "" : endpoint.trim();
        String lower = value.toLowerCase(Locale.US);
        if (lower.startsWith("tcp://")) value = value.substring(6);
        else if (lower.startsWith("kcp://")) value = value.substring(6);
        else if (lower.startsWith("udp://")) value = value.substring(6);
        int slash = value.indexOf('/');
        if (slash >= 0) value = value.substring(0, slash);
        String host = value;
        int colon = value.lastIndexOf(':');
        if (colon > 0 && value.indexOf(']') < colon) host = value.substring(0, colon);
        return "ws://" + host + ":8080";
    }
}
