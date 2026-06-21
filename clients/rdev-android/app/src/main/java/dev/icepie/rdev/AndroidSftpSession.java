package dev.icepie.rdev;

import android.util.Log;

import android.os.Environment;

import java.io.ByteArrayOutputStream;
import java.io.File;
import java.io.IOException;
import java.io.RandomAccessFile;
import java.nio.ByteBuffer;
import java.nio.charset.StandardCharsets;
import java.util.ArrayList;
import java.util.Arrays;
import java.util.HashMap;
import java.util.List;
import java.util.Locale;
import java.util.Map;

final class AndroidSftpSession {
    private static final String TAG = "RDevSftp";
    private static final int SSH_FXP_INIT = 1;
    private static final int SSH_FXP_VERSION = 2;
    private static final int SSH_FXP_OPEN = 3;
    private static final int SSH_FXP_CLOSE = 4;
    private static final int SSH_FXP_READ = 5;
    private static final int SSH_FXP_WRITE = 6;
    private static final int SSH_FXP_LSTAT = 7;
    private static final int SSH_FXP_FSTAT = 8;
    private static final int SSH_FXP_SETSTAT = 9;
    private static final int SSH_FXP_FSETSTAT = 10;
    private static final int SSH_FXP_OPENDIR = 11;
    private static final int SSH_FXP_READDIR = 12;
    private static final int SSH_FXP_REMOVE = 13;
    private static final int SSH_FXP_MKDIR = 14;
    private static final int SSH_FXP_RMDIR = 15;
    private static final int SSH_FXP_REALPATH = 16;
    private static final int SSH_FXP_STAT = 17;
    private static final int SSH_FXP_RENAME = 18;
    private static final int SSH_FXP_STATUS = 101;
    private static final int SSH_FXP_HANDLE = 102;
    private static final int SSH_FXP_DATA = 103;
    private static final int SSH_FXP_NAME = 104;
    private static final int SSH_FXP_ATTRS = 105;

    private static final int SSH_FX_OK = 0;
    private static final int SSH_FX_EOF = 1;
    private static final int SSH_FX_NO_SUCH_FILE = 2;
    private static final int SSH_FX_PERMISSION_DENIED = 3;
    private static final int SSH_FX_FAILURE = 4;
    private static final int SSH_FX_OP_UNSUPPORTED = 8;

    private static final int FXF_READ = 0x00000001;
    private static final int FXF_WRITE = 0x00000002;
    private static final int FXF_APPEND = 0x00000004;
    private static final int FXF_CREAT = 0x00000008;
    private static final int FXF_TRUNC = 0x00000010;
    private static final int FXF_EXCL = 0x00000020;

    private static final int ATTR_SIZE = 0x00000001;
    private static final int ATTR_UIDGID = 0x00000002;
    private static final int ATTR_PERMISSIONS = 0x00000004;
    private static final int ATTR_ACMODTIME = 0x00000008;

    private final RDevAgentService service;
    private final String id;
    private final File home;
    private final Map<String, HandleState> handles = new HashMap<>();
    private byte[] pending = new byte[0];
    private int nextHandle;
    private volatile boolean closed;

    AndroidSftpSession(RDevAgentService service, String id) {
        this.service = service;
        this.id = id;
        this.home = chooseHome(service);
    }

    synchronized void onInput(byte[] data) {
        if (closed || data == null || data.length == 0) return;
        byte[] merged = new byte[pending.length + data.length];
        System.arraycopy(pending, 0, merged, 0, pending.length);
        System.arraycopy(data, 0, merged, pending.length, data.length);
        int pos = 0;
        while (merged.length - pos >= 4) {
            int len = readI32(merged, pos);
            if (len < 1 || len > 64 * 1024 * 1024) {
                Log.w(TAG, "bad packet len=" + len + " session=" + id);
                close();
                return;
            }
            if (merged.length - pos - 4 < len) break;
            byte[] packet = new byte[len];
            System.arraycopy(merged, pos + 4, packet, 0, len);
            handlePacket(packet);
            pos += 4 + len;
        }
        pending = Arrays.copyOfRange(merged, pos, merged.length);
    }

    synchronized void close() {
        closed = true;
        for (HandleState state : handles.values()) state.close();
        handles.clear();
        pending = new byte[0];
    }

    private void handlePacket(byte[] packet) {
        if (packet.length == 0) return;
        Reader reader = new Reader(packet);
        int type = reader.u8();
        if (type == SSH_FXP_INIT) {
            int version = reader.u32();
            Log.i(TAG, "sftp init version=" + version + " session=" + id);
            sendVersion();
            return;
        }
        int requestId = reader.u32();
        try {
            switch (type) {
                case SSH_FXP_REALPATH: realpath(requestId, reader.string()); break;
                case SSH_FXP_STAT: attrs(requestId, resolve(reader.string()), false); break;
                case SSH_FXP_LSTAT: attrs(requestId, resolve(reader.string()), true); break;
                case SSH_FXP_FSTAT: fstat(requestId, reader.string()); break;
                case SSH_FXP_OPENDIR: opendir(requestId, reader.string()); break;
                case SSH_FXP_READDIR: readdir(requestId, reader.string()); break;
                case SSH_FXP_OPEN: open(requestId, reader.string(), reader.u32()); break;
                case SSH_FXP_READ: read(requestId, reader.string(), reader.u64(), reader.u32()); break;
                case SSH_FXP_WRITE: write(requestId, reader.string(), reader.u64(), reader.bytes()); break;
                case SSH_FXP_CLOSE: closeHandle(requestId, reader.string()); break;
                case SSH_FXP_REMOVE: remove(requestId, reader.string()); break;
                case SSH_FXP_MKDIR: mkdir(requestId, reader.string()); break;
                case SSH_FXP_RMDIR: rmdir(requestId, reader.string()); break;
                case SSH_FXP_RENAME: rename(requestId, reader.string(), reader.string()); break;
                case SSH_FXP_SETSTAT:
                case SSH_FXP_FSETSTAT:
                    status(requestId, SSH_FX_OK, "OK");
                    break;
                default:
                    status(requestId, SSH_FX_OP_UNSUPPORTED, "unsupported packet " + type);
            }
        } catch (Throwable t) {
            Log.w(TAG, "sftp packet failed type=" + type + " id=" + requestId, t);
            status(requestId, statusFromThrowable(t), t.getMessage());
        }
    }

    private void sendVersion() {
        Writer writer = new Writer();
        writer.u8(SSH_FXP_VERSION).u32(3);
        sendPacket(writer.bytes());
    }

    private void realpath(int requestId, String path) throws Exception {
        File file = resolve(path);
        File canonical = file.getCanonicalFile();
        name(requestId, new File[] { canonical }, new String[] { toSftpPath(canonical) });
    }

    private void attrs(int requestId, File file, boolean noFollow) throws Exception {
        if (!file.exists()) throw new IOException("no such file: " + file);
        Writer writer = new Writer();
        writer.u8(SSH_FXP_ATTRS).u32(requestId);
        writeAttrs(writer, file);
        sendPacket(writer.bytes());
    }

    private void fstat(int requestId, String handle) throws Exception {
        HandleState state = getHandle(handle);
        File file = state.file;
        if (file == null) throw new IOException("not a file handle");
        attrs(requestId, file, false);
    }

    private void opendir(int requestId, String path) throws Exception {
        File dir = resolve(path);
        File[] files = dir.listFiles();
        if (files == null) throw new IOException("cannot open directory: " + dir);
        Arrays.sort(files, (a, b) -> {
            if (a.isDirectory() != b.isDirectory()) return a.isDirectory() ? -1 : 1;
            return a.getName().compareToIgnoreCase(b.getName());
        });
        String handle = nextHandle();
        handles.put(handle, HandleState.dir(dir, files));
        handle(requestId, handle);
    }

    private void readdir(int requestId, String handle) throws Exception {
        HandleState state = getHandle(handle);
        if (state.entries == null) throw new IOException("not a directory handle");
        if (state.sent) {
            status(requestId, SSH_FX_EOF, "EOF");
            return;
        }
        List<File> files = new ArrayList<>();
        List<String> names = new ArrayList<>();
        files.add(state.file);
        names.add(".");
        File parent = state.file.getParentFile();
        files.add(parent != null ? parent : state.file);
        names.add("..");
        for (File entry : state.entries) {
            files.add(entry);
            names.add(entry.getName());
        }
        state.sent = true;
        name(requestId, files.toArray(new File[0]), names.toArray(new String[0]));
    }

    private void open(int requestId, String path, int flags) throws Exception {
        File file = resolve(path);
        File parent = file.getParentFile();
        if (parent != null && !parent.exists() && (flags & FXF_CREAT) != 0 && !parent.mkdirs()) throw new IOException("mkdir failed: " + parent);
        if ((flags & FXF_EXCL) != 0 && file.exists()) throw new IOException("file exists: " + file);
        String mode = (flags & FXF_WRITE) != 0 || (flags & FXF_APPEND) != 0 ? "rw" : "r";
        RandomAccessFile raf = new RandomAccessFile(file, mode);
        if ((flags & FXF_TRUNC) != 0) raf.setLength(0);
        if ((flags & FXF_APPEND) != 0) raf.seek(raf.length());
        String handle = nextHandle();
        handles.put(handle, HandleState.file(file, raf));
        handle(requestId, handle);
    }

    private void read(int requestId, String handle, long offset, int len) throws Exception {
        HandleState state = getHandle(handle);
        if (state.raf == null) throw new IOException("not a file handle");
        int size = Math.min(Math.max(len, 0), 512 * 1024);
        byte[] data = new byte[size];
        state.raf.seek(offset);
        int n = state.raf.read(data);
        if (n < 0) {
            status(requestId, SSH_FX_EOF, "EOF");
            return;
        }
        Writer writer = new Writer();
        writer.u8(SSH_FXP_DATA).u32(requestId).bytes(data, n);
        sendPacket(writer.bytes());
    }

    private void write(int requestId, String handle, long offset, byte[] data) throws Exception {
        HandleState state = getHandle(handle);
        if (state.raf == null) throw new IOException("not a file handle");
        state.raf.seek(offset);
        state.raf.write(data);
        status(requestId, SSH_FX_OK, "OK");
    }

    private void closeHandle(int requestId, String handle) throws Exception {
        HandleState state = handles.remove(handle);
        if (state != null) state.close();
        status(requestId, SSH_FX_OK, "OK");
    }

    private void remove(int requestId, String path) throws Exception {
        File file = resolve(path);
        if (!file.delete()) throw new IOException("remove failed: " + file);
        status(requestId, SSH_FX_OK, "OK");
    }

    private void mkdir(int requestId, String path) throws Exception {
        File dir = resolve(path);
        if (!dir.mkdir() && !dir.isDirectory()) throw new IOException("mkdir failed: " + dir);
        status(requestId, SSH_FX_OK, "OK");
    }

    private void rmdir(int requestId, String path) throws Exception {
        File dir = resolve(path);
        if (!dir.delete()) throw new IOException("rmdir failed: " + dir);
        status(requestId, SSH_FX_OK, "OK");
    }

    private void rename(int requestId, String oldPath, String newPath) throws Exception {
        File oldFile = resolve(oldPath);
        File newFile = resolve(newPath);
        File parent = newFile.getParentFile();
        if (parent != null && !parent.exists() && !parent.mkdirs()) throw new IOException("mkdir failed: " + parent);
        if (!oldFile.renameTo(newFile)) throw new IOException("rename failed");
        status(requestId, SSH_FX_OK, "OK");
    }

    private void handle(int requestId, String handle) {
        Writer writer = new Writer();
        writer.u8(SSH_FXP_HANDLE).u32(requestId).string(handle);
        sendPacket(writer.bytes());
    }

    private void name(int requestId, File[] files, String[] names) throws Exception {
        Writer writer = new Writer();
        writer.u8(SSH_FXP_NAME).u32(requestId).u32(files.length);
        for (int i = 0; i < files.length; i++) {
            String filename = names[i];
            writer.string(filename);
            writer.string(longName(files[i], filename));
            writeAttrs(writer, files[i]);
        }
        sendPacket(writer.bytes());
    }

    private void status(int requestId, int code, String message) {
        Writer writer = new Writer();
        writer.u8(SSH_FXP_STATUS).u32(requestId).u32(code).string(message == null ? "" : message).string("en-US");
        sendPacket(writer.bytes());
    }

    private void writeAttrs(Writer writer, File file) {
        writer.u32(ATTR_SIZE | ATTR_UIDGID | ATTR_PERMISSIONS | ATTR_ACMODTIME);
        writer.u64(file.isDirectory() ? 0 : file.length());
        writer.u32(0).u32(0);
        writer.u32(mode(file));
        int secs = (int) Math.max(0, Math.min(0xffffffffL, file.lastModified() / 1000L));
        writer.u32(secs).u32(secs);
    }

    private int mode(File file) {
        int kind = file.isDirectory() ? 0040000 : 0100000;
        int perms = 0;
        if (file.canRead()) perms |= 0444;
        if (file.canWrite()) perms |= 0222;
        if (file.isDirectory() || file.canExecute()) perms |= 0111;
        return kind | perms;
    }

    private String longName(File file, String name) {
        char type = file.isDirectory() ? 'd' : '-';
        return String.format(Locale.US, "%crw-rw-rw- 1 0 0 %d %d %s", type, file.isDirectory() ? 0 : file.length(), file.lastModified() / 1000L, name);
    }

    private HandleState getHandle(String handle) throws IOException {
        HandleState state = handles.get(handle);
        if (state == null) throw new IOException("invalid handle");
        return state;
    }

    private String nextHandle() {
        nextHandle++;
        return "h" + nextHandle;
    }

    private File resolve(String path) throws IOException {
        if (path == null || path.length() == 0 || ".".equals(path) || "/".equals(path) || "/sdcard".equals(path)) return home.getCanonicalFile();
        String normalized = path.replace('\\', '/');
        if (normalized.startsWith("/storage/") || normalized.startsWith("/sdcard/") || normalized.startsWith("/data/")) {
            if (normalized.startsWith("/sdcard/")) normalized = "/storage/emulated/0/" + normalized.substring("/sdcard/".length());
            return new File(normalized).getCanonicalFile();
        }
        while (normalized.startsWith("/")) normalized = normalized.substring(1);
        return new File(home, normalized).getCanonicalFile();
    }

    private static File chooseHome(RDevAgentService service) {
        try {
            File publicRoot = Environment.getExternalStorageDirectory();
            if (publicRoot != null && publicRoot.exists() && publicRoot.canRead()) return publicRoot;
        } catch (Throwable ignored) {}
        File external = service.getExternalFilesDir(null);
        return external != null ? external : service.getFilesDir();
    }

    private String toSftpPath(File file) throws IOException {
        return file.getCanonicalPath().replace('\\', '/');
    }

    private int statusFromThrowable(Throwable t) {
        String text = String.valueOf(t.getMessage()).toLowerCase(Locale.US);
        if (text.contains("no such") || text.contains("not found")) return SSH_FX_NO_SUCH_FILE;
        if (text.contains("permission")) return SSH_FX_PERMISSION_DENIED;
        return SSH_FX_FAILURE;
    }

    private void sendPacket(byte[] payload) {
        ByteBuffer packet = ByteBuffer.allocate(4 + payload.length);
        packet.putInt(payload.length);
        packet.put(payload);
        service.sendBinary(RDevProtocol.BIN_DATA, id, packet.array(), packet.array().length);
    }

    private static int readI32(byte[] data, int offset) {
        return ((data[offset] & 0xff) << 24) | ((data[offset + 1] & 0xff) << 16) | ((data[offset + 2] & 0xff) << 8) | (data[offset + 3] & 0xff);
    }

    private static final class HandleState {
        final File file;
        final RandomAccessFile raf;
        final File[] entries;
        boolean sent;

        private HandleState(File file, RandomAccessFile raf, File[] entries) {
            this.file = file;
            this.raf = raf;
            this.entries = entries;
        }

        static HandleState file(File file, RandomAccessFile raf) { return new HandleState(file, raf, null); }
        static HandleState dir(File file, File[] entries) { return new HandleState(file, null, entries); }
        void close() { try { if (raf != null) raf.close(); } catch (IOException ignored) {} }
    }

    private static final class Reader {
        final byte[] data;
        int pos;
        Reader(byte[] data) { this.data = data; }
        int u8() { return data[pos++] & 0xff; }
        int u32() {
            int v = readI32(data, pos);
            pos += 4;
            return v;
        }
        long u64() {
            long hi = u32() & 0xffffffffL;
            long lo = u32() & 0xffffffffL;
            return (hi << 32) | lo;
        }
        byte[] bytes() {
            int len = u32();
            byte[] out = new byte[len];
            System.arraycopy(data, pos, out, 0, len);
            pos += len;
            return out;
        }
        String string() { return new String(bytes(), StandardCharsets.UTF_8); }
    }

    private static final class Writer {
        final ByteArrayOutputStream out = new ByteArrayOutputStream();
        Writer u8(int value) { out.write(value & 0xff); return this; }
        Writer u32(int value) {
            out.write((value >>> 24) & 0xff);
            out.write((value >>> 16) & 0xff);
            out.write((value >>> 8) & 0xff);
            out.write(value & 0xff);
            return this;
        }
        Writer u64(long value) {
            u32((int) (value >>> 32));
            u32((int) value);
            return this;
        }
        Writer bytes(byte[] data, int len) {
            u32(len);
            out.write(data, 0, len);
            return this;
        }
        Writer string(String value) {
            byte[] bytes = value == null ? new byte[0] : value.getBytes(StandardCharsets.UTF_8);
            return bytes(bytes, bytes.length);
        }
        byte[] bytes() { return out.toByteArray(); }
    }
}
