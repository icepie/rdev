package dev.icepie.rdev;

import android.util.Base64;
import android.util.Log;

import java.io.ByteArrayOutputStream;
import java.io.EOFException;
import java.io.IOException;
import java.io.InputStream;
import java.io.OutputStream;
import java.net.Socket;
import java.net.URI;
import java.security.MessageDigest;
import java.security.SecureRandom;
import java.util.Locale;

import javax.net.ssl.SSLSocketFactory;

final class RDevWebSocketClient {
    interface Listener {
        void onOpen();
        void onText(String text);
        void onBinary(byte[] data);
        void onClosed(Exception error);
    }

    private static final String TAG = "RDevWs";
    private static final String WS_GUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11";
    private final String rawUrl;
    private final Listener listener;
    private final Object writeLock = new Object();
    private volatile boolean running;
    private Socket socket;
    private InputStream in;
    private OutputStream out;
    private Thread thread;
    private Thread pingThread;
    private final SecureRandom random = new SecureRandom();

    RDevWebSocketClient(String rawUrl, Listener listener) {
        this.rawUrl = normalizeUrl(rawUrl);
        this.listener = listener;
    }

    void connect() {
        running = true;
        thread = new Thread(this::runLoop, "rdev-ws");
        thread.start();
    }

    void close() {
        running = false;
        final Socket socketToClose = socket;
        socket = null;
        if (socketToClose == null) return;
        new Thread(() -> {
            try { socketToClose.close(); } catch (IOException ignored) {}
        }, "rdev-ws-close").start();
    }

    void sendText(String text) throws IOException {
        sendFrame(0x1, text.getBytes("UTF-8"));
    }

    void sendBinary(byte[] data) throws IOException {
        sendFrame(0x2, data);
    }

    private void runLoop() {
        Exception closeError = null;
        try {
            String currentUrl = rawUrl;
            for (int redirects = 0; redirects <= 5; redirects++) {
                URI uri = URI.create(currentUrl);
                boolean tls = "wss".equalsIgnoreCase(uri.getScheme());
                int port = uri.getPort() >= 0 ? uri.getPort() : (tls ? 443 : 80);
                String host = uri.getHost();
                if (host == null || host.length() == 0) throw new IOException("missing websocket host: " + currentUrl);
                socket = tls ? SSLSocketFactory.getDefault().createSocket(host, port) : new Socket(host, port);
                socket.setTcpNoDelay(true);
                socket.setKeepAlive(true);
                socket.setSoTimeout(45000);
                in = socket.getInputStream();
                out = socket.getOutputStream();
                String key = randomKey();
                String header = handshake(uri, host, port, tls, key);
                int status = statusCode(header);
                if (status == 101) {
                    String expected = acceptKey(key);
                    if (!header.toLowerCase(Locale.US).contains("sec-websocket-accept: " + expected.toLowerCase(Locale.US))) {
                        throw new IOException("websocket accept mismatch");
                    }
                    Log.i(TAG, "connected " + currentUrl);
                    startPingLoop();
                    listener.onOpen();
                    readFrames();
                    return;
                }
                if (isRedirect(status)) {
                    String location = headerValue(header, "location");
                    closeSocketQuietly();
                    if (location == null || location.length() == 0) throw new IOException("websocket redirect without location");
                    currentUrl = normalizeUrl(resolveRedirect(currentUrl, location));
                    Log.i(TAG, "websocket redirect to " + currentUrl);
                    continue;
                }
                throw new IOException("websocket handshake failed: " + firstLine(header));
            }
            throw new IOException("too many websocket redirects");
        } catch (Exception e) {
            closeError = e;
            if (running) Log.w(TAG, "websocket closed", e);
        } finally {
            running = false;
            stopPingLoop();
            closeSocketQuietly();
            listener.onClosed(closeError);
        }
    }

    private void closeSocketQuietly() {
        try { if (socket != null) socket.close(); } catch (IOException ignored) {}
        socket = null;
        in = null;
        out = null;
    }

    private String handshake(URI uri, String host, int port, boolean tls, String key) throws Exception {
        String path = uri.getRawPath();
        if (path == null || path.length() == 0) path = "/";
        if (uri.getRawQuery() != null && uri.getRawQuery().length() > 0) path += "?" + uri.getRawQuery();
        String hostHeader = host;
        if ((tls && port != 443) || (!tls && port != 80)) hostHeader += ":" + port;
        String req = "GET " + path + " HTTP/1.1\r\n" +
            "Host: " + hostHeader + "\r\n" +
            "Upgrade: websocket\r\n" +
            "Connection: Upgrade\r\n" +
            "Sec-WebSocket-Version: 13\r\n" +
            "Sec-WebSocket-Key: " + key + "\r\n" +
            "User-Agent: rdev-android/0.1\r\n" +
            "\r\n";
        out.write(req.getBytes("US-ASCII"));
        out.flush();
        return readHttpHeader();
    }

    private String readHttpHeader() throws IOException {
        ByteArrayOutputStream buf = new ByteArrayOutputStream();
        int state = 0;
        while (true) {
            int b = in.read();
            if (b < 0) throw new EOFException("handshake eof");
            buf.write(b);
            if ((state == 0 || state == 2) && b == '\r') state++;
            else if ((state == 1 || state == 3) && b == '\n') state++;
            else state = 0;
            if (state == 4) return buf.toString("US-ASCII");
            if (buf.size() > 16384) throw new IOException("handshake too large");
        }
    }

    private void readFrames() throws IOException {
        while (running) {
            int b0 = in.read();
            if (b0 < 0) throw new EOFException("websocket eof");
            int b1 = readByte();
            int opcode = b0 & 0x0f;
            long len = b1 & 0x7f;
            if (len == 126) len = ((long) readByte() << 8) | readByte();
            else if (len == 127) {
                len = 0;
                for (int i = 0; i < 8; i++) len = (len << 8) | readByte();
            }
            byte[] mask = null;
            if ((b1 & 0x80) != 0) {
                mask = new byte[] {(byte) readByte(), (byte) readByte(), (byte) readByte(), (byte) readByte()};
            }
            if (len > 16 * 1024 * 1024) throw new IOException("frame too large: " + len);
            byte[] payload = readFully((int) len);
            if (mask != null) for (int i = 0; i < payload.length; i++) payload[i] ^= mask[i & 3];
            if (opcode == 0x1) listener.onText(new String(payload, "UTF-8"));
            else if (opcode == 0x2) listener.onBinary(payload);
            else if (opcode == 0x8) return;
            else if (opcode == 0x9) sendFrame(0xA, payload);
        }
    }

    private void startPingLoop() {
        stopPingLoop();
        pingThread = new Thread(() -> {
            while (running) {
                try {
                    Thread.sleep(10000);
                    if (running) sendFrame(0x9, new byte[] {'r', 'd', 'e', 'v'});
                } catch (InterruptedException e) {
                    Thread.currentThread().interrupt();
                    return;
                } catch (IOException e) {
                    Log.w(TAG, "websocket ping failed", e);
                    close();
                    return;
                }
            }
        }, "rdev-ws-ping");
        pingThread.start();
    }

    private void stopPingLoop() {
        Thread thread = pingThread;
        pingThread = null;
        if (thread != null) thread.interrupt();
    }

    private void sendFrame(int opcode, byte[] payload) throws IOException {
        if (!running || out == null) throw new IOException("websocket not connected");
        byte[] mask = new byte[4];
        random.nextBytes(mask);
        synchronized (writeLock) {
            out.write(0x80 | (opcode & 0x0f));
            int len = payload.length;
            if (len < 126) out.write(0x80 | len);
            else if (len <= 0xffff) {
                out.write(0x80 | 126);
                out.write((len >>> 8) & 0xff);
                out.write(len & 0xff);
            } else {
                long length = payload.length;
                out.write(0x80 | 127);
                for (int i = 7; i >= 0; i--) out.write((int) ((length >>> (8 * i)) & 0xff));
            }
            out.write(mask);
            byte[] masked = new byte[payload.length];
            for (int i = 0; i < payload.length; i++) masked[i] = (byte) (payload[i] ^ mask[i & 3]);
            out.write(masked);
            out.flush();
        }
    }

    private int readByte() throws IOException {
        int b = in.read();
        if (b < 0) throw new EOFException("websocket eof");
        return b & 0xff;
    }

    private byte[] readFully(int len) throws IOException {
        byte[] data = new byte[len];
        int off = 0;
        while (off < len) {
            int n = in.read(data, off, len - off);
            if (n < 0) throw new EOFException("websocket payload eof");
            off += n;
        }
        return data;
    }

    private String randomKey() {
        byte[] key = new byte[16];
        new SecureRandom().nextBytes(key);
        return Base64.encodeToString(key, Base64.NO_WRAP);
    }

    private String acceptKey(String key) throws Exception {
        MessageDigest sha1 = MessageDigest.getInstance("SHA-1");
        return Base64.encodeToString(sha1.digest((key + WS_GUID).getBytes("US-ASCII")), Base64.NO_WRAP);
    }

    private String firstLine(String header) {
        int pos = header.indexOf('\n');
        return pos >= 0 ? header.substring(0, pos).trim() : header.trim();
    }

    private int statusCode(String header) {
        String line = firstLine(header);
        String[] parts = line.split(" ");
        if (parts.length < 2) return 0;
        try { return Integer.parseInt(parts[1]); } catch (Throwable ignored) { return 0; }
    }

    private boolean isRedirect(int status) {
        return status == 301 || status == 302 || status == 303 || status == 307 || status == 308;
    }

    private String headerValue(String header, String name) {
        String needle = name.toLowerCase(Locale.US) + ":";
        for (String line : header.split("\r?\n")) {
            String lower = line.toLowerCase(Locale.US);
            if (lower.startsWith(needle)) {
                return line.substring(line.indexOf(':') + 1).trim();
            }
        }
        return null;
    }

    private String resolveRedirect(String currentUrl, String location) {
        URI base = URI.create(currentUrl);
        URI rel = URI.create(location);
        URI next = base.resolve(rel);
        String scheme = next.getScheme();
        if (scheme == null) return next.toString();
        if (scheme.equalsIgnoreCase("http")) return "ws://" + next.toString().substring(7);
        if (scheme.equalsIgnoreCase("https")) return "wss://" + next.toString().substring(8);
        return next.toString();
    }

    private static String normalizeUrl(String value) {
        String url = value == null ? "" : value.trim();
        if (url.startsWith("https://")) url = "wss://" + url.substring(8);
        else if (url.startsWith("http://")) url = "ws://" + url.substring(7);
        else if (!url.startsWith("ws://") && !url.startsWith("wss://")) url = "ws://" + url;
        try {
            URI uri = URI.create(url);
            String path = uri.getRawPath();
            String query = uri.getQuery();
            if (path == null || path.length() == 0 || "/".equals(path)) path = "/ws";
            else if (!path.endsWith("/ws")) path = path.endsWith("/") ? path + "ws" : path + "/ws";
            URI rebuilt = new URI(uri.getScheme(), uri.getUserInfo(), uri.getHost(), uri.getPort(), path, query, uri.getFragment());
            return rebuilt.toString();
        } catch (Throwable ignored) {
            if (!url.endsWith("/ws")) url += "/ws";
            return url;
        }
    }
}
