package dev.icepie.rdev;

import java.io.EOFException;
import java.io.IOException;
import java.io.InputStream;
import java.io.OutputStream;

final class RDevStreamFrame {
    static final int KIND_JSON = 1;
    static final int KIND_BINARY = 2;
    static final int KIND_PING = 3;
    static final int KIND_PONG = 4;
    static final int KIND_CLOSE = 5;
    private static final int MAX_FRAME = 16 * 1024 * 1024;

    final int kind;
    final byte[] payload;

    RDevStreamFrame(int kind, byte[] payload) {
        this.kind = kind;
        this.payload = payload == null ? new byte[0] : payload;
    }

    static RDevStreamFrame read(InputStream in) throws IOException {
        byte[] header = readFully(in, 5);
        int len = ((header[1] & 0xff) << 24) | ((header[2] & 0xff) << 16) | ((header[3] & 0xff) << 8) | (header[4] & 0xff);
        if (len < 0 || len > MAX_FRAME) throw new IOException("stream frame too large: " + len);
        return new RDevStreamFrame(header[0] & 0xff, readFully(in, len));
    }

    static void write(OutputStream out, int kind, byte[] payload) throws IOException {
        if (payload == null) payload = new byte[0];
        if (payload.length > MAX_FRAME) throw new IOException("stream frame too large: " + payload.length);
        out.write(kind & 0xff);
        out.write((payload.length >>> 24) & 0xff);
        out.write((payload.length >>> 16) & 0xff);
        out.write((payload.length >>> 8) & 0xff);
        out.write(payload.length & 0xff);
        out.write(payload);
        out.flush();
    }

    private static byte[] readFully(InputStream in, int len) throws IOException {
        byte[] data = new byte[len];
        int off = 0;
        while (off < len) {
            int n = in.read(data, off, len - off);
            if (n < 0) throw new EOFException("stream frame eof");
            off += n;
        }
        return data;
    }
}
