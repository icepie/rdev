package dev.icepie.rdev;

import android.os.Environment;
import android.util.Log;

import org.json.JSONObject;

import java.io.IOException;
import java.io.InputStream;
import java.io.OutputStream;
import java.util.ArrayDeque;
import java.util.Map;
import java.util.Queue;
import java.util.concurrent.ConcurrentHashMap;

final class AndroidTerminalManager {
    private static final String TAG = "RDevTerm";
    private static final int MAX_SESSIONS = 8;
    private final RDevAgentService service;
    private final Map<String, Session> sessions = new ConcurrentHashMap<>();
    private final Map<String, AndroidSftpSession> sftpSessions = new ConcurrentHashMap<>();

    AndroidTerminalManager(RDevAgentService service) {
        this.service = service;
    }

    void handleNewSession(JSONObject msg) {
        String sessionId = msg.optString("sessionId", "");
        if (sessionId.length() == 0) return;
        close(sessionId);
        String subsystem = msg.optString("subsystem", "");
        if ("sftp".equals(subsystem)) {
            AndroidSftpSession sftp = new AndroidSftpSession(service, sessionId);
            sftpSessions.put(sessionId, sftp);
            Log.i(TAG, "sftp session started id=" + sessionId);
            return;
        }
        if (sessions.size() >= MAX_SESSIONS) {
            service.sendError(sessionId, "too many terminal sessions");
            service.sendClose(sessionId);
            return;
        }
        try {
            String command = msg.optString("command", "");
            String cwd = safeDirectory(msg.optString("cwd", homePath())).getAbsolutePath();
            if (command.length() == 0) {
                Session session = new Session(sessionId, null, cwd, true);
                sessions.put(sessionId, session);
                sendBanner(session);
                Log.i(TAG, "line session started id=" + sessionId + " cwd=" + cwd);
                return;
            }

            ProcessBuilder builder = shellBuilder(command, cwd);
            Process process = builder.start();
            Session session = new Session(sessionId, process, cwd, false);
            sessions.put(sessionId, session);
            startReader(session, process.getInputStream(), false);
            startReader(session, process.getErrorStream(), true);
            new Thread(() -> waitSession(session), "rdev-term-wait").start();
            Log.i(TAG, "session started id=" + sessionId + " command=" + command);
        } catch (Exception e) {
            Log.w(TAG, "session start failed id=" + sessionId, e);
            service.sendClose(sessionId);
        }
    }

    void handleInput(String sessionId, byte[] data) {
        AndroidSftpSession sftp = sftpSessions.get(sessionId);
        if (sftp != null) {
            sftp.onInput(data);
            return;
        }
        Session session = sessions.get(sessionId);
        if (session == null || data.length == 0) {
            Log.w(TAG, "stdin ignored id=" + sessionId + " session=" + (session != null) + " bytes=" + data.length);
            return;
        }
        try {
            byte[] normalized = normalizeInput(data);
            if (session.lineMode) {
                handleLineInput(session, normalized);
                return;
            }
            echoInput(session, normalized);
            OutputStream stdin = session.process.getOutputStream();
            stdin.write(normalized);
            stdin.flush();
        } catch (IOException e) {
            close(sessionId);
        }
    }

    void handleStdinClose(String sessionId) {
        AndroidSftpSession sftp = sftpSessions.get(sessionId);
        if (sftp != null) return;
        Session session = sessions.get(sessionId);
        if (session == null || session.process == null) return;
        try { session.process.getOutputStream().close(); } catch (IOException ignored) {}
    }

    void handleResize(String sessionId, int rows, int cols) {
        Log.d(TAG, "resize ignored id=" + sessionId + " rows=" + rows + " cols=" + cols);
    }

    void close(String sessionId) {
        AndroidSftpSession sftp = sftpSessions.remove(sessionId);
        if (sftp != null) {
            sftp.close();
            service.sendClose(sessionId);
            return;
        }
        Session session = sessions.remove(sessionId);
        if (session == null) return;
        session.closed = true;
        if (session.process == null) return;
        try { session.process.getOutputStream().close(); } catch (IOException ignored) {}
        try { session.process.destroy(); } catch (Throwable ignored) {}
        new Thread(() -> {
            try { Thread.sleep(1200); } catch (InterruptedException e) { Thread.currentThread().interrupt(); }
            try { session.process.destroyForcibly(); } catch (Throwable ignored) {}
        }, "rdev-term-kill").start();
    }

    void closeAll() {
        for (String id : sessions.keySet()) close(id);
        for (String id : sftpSessions.keySet()) close(id);
    }

    private void handleLineInput(Session session, byte[] data) {
        String text = new String(data, java.nio.charset.StandardCharsets.UTF_8);
        for (int i = 0; i < text.length(); i++) {
            char ch = text.charAt(i);
            if (consumeEscapeInput(session, ch)) continue;
            if (ch == 0x1b) {
                session.escapeInput = true;
                continue;
            }
            if (ch == 0x03) {
                Process active;
                synchronized (session) {
                    session.input.setLength(0);
                    session.pendingLines.clear();
                    active = session.activeProcess;
                    if (active == null) session.commandRunning = false;
                }
                if (active != null) {
                    try { active.destroy(); } catch (Throwable ignored) {}
                    new Thread(() -> {
                        try { Thread.sleep(500); } catch (InterruptedException e) { Thread.currentThread().interrupt(); }
                        try { active.destroyForcibly(); } catch (Throwable ignored) {}
                    }, "rdev-term-interrupt").start();
                    sendRaw(session, "^C\r\n");
                } else {
                    sendRaw(session, "^C\r\n");
                    sendPrompt(session);
                }
                continue;
            }
            if (ch == 0x0c) {
                sendRaw(session, "\u001b[2J\u001b[H");
                sendPrompt(session);
                continue;
            }
            if (ch == 0x15) {
                int length;
                synchronized (session) {
                    length = session.input.length();
                    session.input.setLength(0);
                }
                if (length > 0) sendRaw(session, repeat("\b \b", length));
                continue;
            }
            if (ch == '\n' || ch == '\r') {
                synchronized (session) {
                    String line = session.input.toString();
                    session.input.setLength(0);
                    session.pendingLines.add(line);
                    sendRaw(session, "\r\n");
                    if (session.commandRunning) continue;
                    session.commandRunning = true;
                }
                new Thread(() -> drainLineCommands(session), "rdev-term-line").start();
            } else if (ch == '\b' || ch == 0x7f) {
                synchronized (session) {
                    int length = session.input.length();
                    if (length > 0) {
                        session.input.setLength(length - 1);
                        sendRaw(session, "\b \b");
                    }
                }
            } else if (ch >= 0x20 || ch == '\t') {
                synchronized (session) { session.input.append(ch); }
                sendRaw(session, String.valueOf(ch));
            }
        }
    }

    private void drainLineCommands(Session session) {
        while (!session.closed) {
            String line;
            synchronized (session) {
                line = session.pendingLines.poll();
                if (line == null) {
                    session.commandRunning = false;
                    return;
                }
            }
            runLine(session, line);
        }
        synchronized (session) { session.commandRunning = false; }
    }

    private void runLine(Session session, String line) {
        String command = line.trim();
        if (command.length() == 0) {
            sendPrompt(session);
            return;
        }
        if (handleCd(session, command)) return;
        Process process = null;
        try {
            process = shellBuilder(command, session.cwd).start();
            session.activeProcess = process;
            Thread stdout = startProcessReader(session, process.getInputStream(), false);
            Thread stderr = startProcessReader(session, process.getErrorStream(), true);
            process.waitFor();
            stdout.join(1500);
            stderr.join(1500);
        } catch (Exception e) {
            sendLine(session, "rdev: " + e.getMessage() + "\n", true);
        } finally {
            session.activeProcess = null;
            if (process != null) {
                try { process.destroy(); } catch (Throwable ignored) {}
            }
            sendPrompt(session);
        }
    }

    private boolean handleCd(Session session, String command) {
        if (!"cd".equals(command) && !command.startsWith("cd ")) return false;
        String target = command.length() == 2 ? homePath() : command.substring(3).trim();
        if (target.length() == 0 || "~".equals(target)) target = homePath();
        java.io.File dir = target.startsWith("/") ? new java.io.File(target) : new java.io.File(session.cwd, target);
        dir = safeDirectory(dir.getAbsolutePath());
        if (!dir.isDirectory()) {
            sendLine(session, "cd: " + target + ": No such directory\n", true);
        } else if (!dir.canRead()) {
            sendLine(session, "cd: " + target + ": Permission denied\n", true);
        } else {
            session.cwd = dir.getAbsolutePath();
        }
        sendPrompt(session);
        return true;
    }

    private ProcessBuilder shellBuilder(String command, String cwd) {
        ProcessBuilder builder = new ProcessBuilder("/system/bin/sh", "-c", command);
        builder.directory(safeDirectory(cwd));
        builder.environment().put("TERM", "xterm-256color");
        builder.environment().put("HOME", homePath());
        builder.environment().put("TMPDIR", service.getCacheDir().getAbsolutePath());
        builder.environment().put("PATH", "/system/bin:/system/xbin:/vendor/bin:/product/bin:/apex/com.android.runtime/bin");
        builder.redirectErrorStream(false);
        return builder;
    }

    private java.io.File safeDirectory(String path) {
        try {
            java.io.File dir = new java.io.File(canonicalPath(path));
            if (dir.isDirectory() && dir.canRead()) return dir;
        } catch (Throwable ignored) {}
        return new java.io.File(homePath());
    }

    private String canonicalPath(String path) {
        try {
            if (path == null || path.length() == 0 || "/".equals(path) || ".".equals(path)) return homePath();
            if (path.equals("/sdcard")) return Environment.getExternalStorageDirectory().getAbsolutePath();
            if (path.startsWith("/sdcard/")) return Environment.getExternalStorageDirectory().getAbsolutePath() + path.substring(7);
            return new java.io.File(path).getCanonicalPath();
        } catch (IOException e) {
            return path == null || path.length() == 0 ? homePath() : path;
        }
    }

    private void startReader(Session session, InputStream input, boolean stderr) {
        new Thread(() -> {
            byte[] buffer = new byte[8192];
            try {
                while (!session.closed) {
                    int n = input.read(buffer);
                    if (n < 0) return;
                    if (n > 0) {
                        sendOutput(session, buffer, n, stderr);
                        if (!stderr) sendPromptIfNeeded(session);
                    }
                }
            } catch (IOException ignored) {
            } finally {
                try { input.close(); } catch (IOException ignored) {}
            }
        }, stderr ? "rdev-term-stderr" : "rdev-term-stdout").start();
    }

    private Thread startProcessReader(Session session, InputStream input, boolean stderr) {
        Thread thread = new Thread(() -> {
            byte[] buffer = new byte[8192];
            try {
                int n;
                while (!session.closed && (n = input.read(buffer)) >= 0) {
                    if (n > 0) sendOutput(session, buffer, n, stderr);
                }
            } catch (IOException ignored) {
            } finally {
                try { input.close(); } catch (IOException ignored) {}
            }
        }, stderr ? "rdev-term-line-stderr" : "rdev-term-line-stdout");
        thread.start();
        return thread;
    }

    private void sendBanner(Session session) {
        sendRaw(session, "RDev Android shell (/system/bin/sh, virtual PTY line mode)\r\n");
        sendPrompt(session);
    }

    private byte[] normalizeInput(byte[] data) {
        byte[] out = new byte[data.length];
        int size = 0;
        boolean changed = false;
        for (int i = 0; i < data.length; i++) {
            byte b = data[i];
            if (b == '\r') {
                changed = true;
                if (i + 1 < data.length && data[i + 1] == '\n') continue;
                out[size++] = '\n';
            } else {
                out[size++] = b;
            }
        }
        if (!changed) return data;
        byte[] normalized = new byte[size];
        System.arraycopy(out, 0, normalized, 0, size);
        return normalized;
    }

    private void echoInput(Session session, byte[] data) {
        String text = new String(data, java.nio.charset.StandardCharsets.UTF_8);
        StringBuilder out = new StringBuilder();
        boolean submitted = false;
        for (int i = 0; i < text.length(); i++) {
            char ch = text.charAt(i);
            if (ch == '\r' || ch == '\n') {
                out.append("\r\n");
                submitted = true;
            } else if (ch == '\b' || ch == 0x7f) out.append("\b \b");
            else if (ch >= 0x20 || ch == '\t') out.append(ch);
        }
        if (submitted) session.awaitingPrompt = true;
        if (out.length() > 0) sendRaw(session, out.toString());
    }

    private void sendPromptIfNeeded(Session session) {
        if (!session.awaitingPrompt || session.closed) return;
        sendPrompt(session);
    }

    private void sendPrompt(Session session) {
        synchronized (session) {
            session.awaitingPrompt = false;
            if (!session.lineStart) sendRawLocked(session, "\r\n", false);
            sendRawLocked(session, session.cwd + " $ ", false);
            session.promptVisible = true;
        }
    }

    private void sendLine(Session session, String text, boolean stderr) {
        byte[] data = text.getBytes(java.nio.charset.StandardCharsets.UTF_8);
        sendOutput(session, data, data.length, stderr);
    }

    private void sendOutput(Session session, byte[] data, int length, boolean stderr) {
        if (length <= 0 || session.closed) return;
        String text = new String(data, 0, length, java.nio.charset.StandardCharsets.UTF_8);
        String normalized = normalizeTerminalOutput(text);
        synchronized (session) {
            session.promptVisible = false;
            sendRawLocked(session, normalized, stderr);
        }
    }

    private void sendRaw(Session session, String text) {
        synchronized (session) { sendRawLocked(session, text, false); }
    }

    private void sendRawLocked(Session session, String text, boolean stderr) {
        if (text == null || text.length() == 0 || session.closed) return;
        updateLineState(session, text);
        byte[] data = text.getBytes(java.nio.charset.StandardCharsets.UTF_8);
        service.sendBinary(stderr ? RDevProtocol.BIN_STDERR : RDevProtocol.BIN_DATA, session.id, data, data.length);
    }

    private String normalizeTerminalOutput(String text) {
        StringBuilder out = new StringBuilder(text.length() + 16);
        for (int i = 0; i < text.length(); i++) {
            char ch = text.charAt(i);
            if (ch == '\r') {
                out.append('\r');
                if (i + 1 < text.length() && text.charAt(i + 1) == '\n') {
                    out.append('\n');
                    i++;
                }
            } else if (ch == '\n') {
                out.append("\r\n");
            } else {
                out.append(ch);
            }
        }
        return out.toString();
    }

    private void updateLineState(Session session, String text) {
        if (text.length() == 0) return;
        char last = text.charAt(text.length() - 1);
        session.lineStart = last == '\n' || last == '\r';
        if (!session.lineStart) session.promptVisible = false;
    }

    private boolean consumeEscapeInput(Session session, char ch) {
        if (!session.escapeInput) return false;
        if (session.escapeCsi) {
            if (ch >= 0x40 && ch <= 0x7e) {
                session.escapeInput = false;
                session.escapeCsi = false;
            }
            return true;
        }
        if (ch == '[' || ch == 'O') {
            session.escapeCsi = true;
            return true;
        }
        session.escapeInput = false;
        return true;
    }

    private String repeat(String text, int count) {
        StringBuilder out = new StringBuilder(text.length() * Math.max(0, count));
        for (int i = 0; i < count; i++) out.append(text);
        return out.toString();
    }

    private String homePath() {
        try {
            java.io.File publicRoot = Environment.getExternalStorageDirectory();
            if (publicRoot != null && publicRoot.exists() && publicRoot.canRead()) return publicRoot.getAbsolutePath();
        } catch (Throwable ignored) {}
        java.io.File external = service.getExternalFilesDir(null);
        return external != null ? external.getAbsolutePath() : service.getFilesDir().getAbsolutePath();
    }

    private void waitSession(Session session) {
        int code = -1;
        try { code = session.process.waitFor(); } catch (InterruptedException e) { Thread.currentThread().interrupt(); }
        if (sessions.remove(session.id, session)) {
            session.closed = true;
            service.sendExitCode(session.id, code);
            service.sendClose(session.id);
        }
    }

    private static final class Session {
        final String id;
        final Process process;
        final boolean lineMode;
        final StringBuilder input = new StringBuilder();
        final Queue<String> pendingLines = new ArrayDeque<>();
        volatile String cwd;
        volatile boolean closed;
        volatile boolean awaitingPrompt;
        volatile boolean commandRunning;
        boolean lineStart = true;
        boolean promptVisible;
        boolean escapeInput;
        boolean escapeCsi;
        volatile Process activeProcess;

        Session(String id, Process process, String cwd, boolean lineMode) {
            this.id = id;
            this.process = process;
            this.cwd = cwd;
            this.lineMode = lineMode;
        }
    }
}
