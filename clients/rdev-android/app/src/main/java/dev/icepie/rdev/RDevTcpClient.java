package dev.icepie.rdev;

import android.util.Log;

import java.io.IOException;
import java.io.InputStream;
import java.io.OutputStream;
import java.net.Socket;
import java.net.URI;

final class RDevTcpClient implements RDevControlConnection {
    private static final String TAG = "RDevTcp";
    private final String endpoint;
    private final Listener listener;
    private final Object writeLock = new Object();
    private volatile boolean running;
    private Socket socket;
    private InputStream in;
    private OutputStream out;
    private Thread thread;
    private Thread pingThread;

    RDevTcpClient(String endpoint, Listener listener) {
        this.endpoint = normalize(endpoint);
        this.listener = listener;
    }

    @Override public void connect() {
        running = true;
        thread = new Thread(this::runLoop, "rdev-tcp");
        thread.start();
    }

    @Override public void close() {
        running = false;
        Socket s = socket;
        socket = null;
        if (s != null) {
            try { s.close(); } catch (IOException ignored) {}
        }
    }

    @Override public void sendText(String text) throws IOException {
        send(RDevStreamFrame.KIND_JSON, text.getBytes("UTF-8"));
    }

    @Override public void sendBinary(byte[] data) throws IOException {
        send(RDevStreamFrame.KIND_BINARY, data);
    }

    private void runLoop() {
        Exception closeError = null;
        try {
            URI uri = URI.create(endpoint);
            String host = uri.getHost();
            int port = uri.getPort();
            if (host == null || host.length() == 0 || port <= 0) throw new IOException("bad tcp endpoint: " + endpoint);
            socket = new Socket(host, port);
            socket.setTcpNoDelay(true);
            socket.setKeepAlive(true);
            socket.setSoTimeout(75000);
            in = socket.getInputStream();
            out = socket.getOutputStream();
            Log.i(TAG, "connected " + endpoint);
            startPingLoop();
            listener.onOpen();
            while (running) {
                RDevStreamFrame frame = RDevStreamFrame.read(in);
                if (frame.kind == RDevStreamFrame.KIND_JSON) listener.onText(new String(frame.payload, "UTF-8"));
                else if (frame.kind == RDevStreamFrame.KIND_BINARY) listener.onBinary(frame.payload);
                else if (frame.kind == RDevStreamFrame.KIND_PING) send(RDevStreamFrame.KIND_PONG, frame.payload);
                else if (frame.kind == RDevStreamFrame.KIND_CLOSE) break;
            }
        } catch (Exception e) {
            closeError = e;
            if (running) Log.w(TAG, "tcp closed", e);
        } finally {
            running = false;
            stopPingLoop();
            close();
            listener.onClosed(closeError);
        }
    }

    private void send(int kind, byte[] payload) throws IOException {
        if (!running || out == null) throw new IOException("tcp not connected");
        synchronized (writeLock) {
            RDevStreamFrame.write(out, kind, payload);
        }
    }

    private void startPingLoop() {
        stopPingLoop();
        pingThread = new Thread(() -> {
            while (running) {
                try {
                    Thread.sleep(25000);
                    if (running) send(RDevStreamFrame.KIND_PING, new byte[] {'r','d','e','v'});
                } catch (InterruptedException e) {
                    Thread.currentThread().interrupt();
                    return;
                } catch (IOException e) {
                    Log.w(TAG, "tcp ping failed", e);
                    close();
                    return;
                }
            }
        }, "rdev-tcp-ping");
        pingThread.start();
    }

    private void stopPingLoop() {
        Thread t = pingThread;
        pingThread = null;
        if (t != null) t.interrupt();
    }

    static String normalize(String raw) {
        String value = raw == null ? "" : raw.trim();
        if (!value.contains("://")) value = "tcp://" + value;
        if (value.startsWith("tcp:///")) value = "tcp://" + value.substring(7).replaceFirst("^/+", "");
        return value;
    }
}
