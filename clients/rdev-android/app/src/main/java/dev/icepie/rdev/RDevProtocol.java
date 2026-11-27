package dev.icepie.rdev;

import java.io.ByteArrayOutputStream;
import java.nio.ByteBuffer;
import java.nio.charset.StandardCharsets;

final class RDevProtocol {
    static final int BIN_DATA = 0x01;
    static final int BIN_STDERR = 0x02;
    static final int BIN_TCP_DATA = 0x03;
    static final int BIN_FILE_UPLOAD_CHUNK = 0x20;
    static final int BIN_FILE_UPLOAD_ACK = 0x21;
    static final int BIN_FILE_DOWNLOAD_CHUNK = 0x22;
    static final int BIN_FILE_TRANSFER_END = 0x23;
    static final int BIN_FILE_TRANSFER_CANCEL = 0x24;

    final int type;
    final String id;
    /**
     * Frame data bytes. For session/forward frames this is the whole body; for
     * offset file frames (0x20..0x24) it is the same array as
     * {@link #offsetPayload}, i.e. the bytes after the offset word.
     */
    final byte[] payload;
    final long offset;
    final byte[] offsetPayload;

    private RDevProtocol(int type, String id, byte[] payload, long offset, byte[] offsetPayload) {
        this.type = type;
        this.id = id;
        this.payload = payload;
        this.offset = offset;
        this.offsetPayload = offsetPayload;
    }

    /** Only these types carry the 8-byte offset word; everything else is payload. */
    static boolean isOffsetType(int type) {
        return type >= BIN_FILE_UPLOAD_CHUNK && type <= BIN_FILE_TRANSFER_CANCEL;
    }

    static RDevProtocol decode(byte[] raw) throws Exception {
        if (raw == null || raw.length < 2) throw new IllegalArgumentException("binary frame too short");
        int type = raw[0] & 0xff;
        int idLen = raw[1] & 0xff;
        int body = 2 + idLen;
        if (raw.length < body) throw new IllegalArgumentException("binary frame id truncated");
        String id = new String(raw, 2, idLen, StandardCharsets.UTF_8);
        if (!isOffsetType(type)) {
            byte[] payload = new byte[raw.length - body];
            System.arraycopy(raw, body, payload, 0, payload.length);
            return new RDevProtocol(type, id, payload, 0L, payload);
        }
        if (raw.length < body + 8) throw new IllegalArgumentException("binary offset frame too short");
        long offset = ByteBuffer.wrap(raw, body, 8).getLong();
        byte[] payload = new byte[raw.length - body - 8];
        System.arraycopy(raw, body + 8, payload, 0, payload.length);
        return new RDevProtocol(type, id, payload, offset, payload);
    }

    static byte[] encode(int type, String id, byte[] data, int len) throws Exception {
        byte[] idBytes = id.getBytes(StandardCharsets.UTF_8);
        ByteArrayOutputStream out = new ByteArrayOutputStream(2 + idBytes.length + len);
        out.write(type & 0xff);
        out.write(idBytes.length & 0xff);
        out.write(idBytes);
        out.write(data, 0, len);
        return out.toByteArray();
    }

    /** Writes the final frame in one array; no intermediate body copy. */
    static byte[] encodeOffset(int type, String id, long offset, byte[] data, int len) throws Exception {
        byte[] idBytes = id.getBytes(StandardCharsets.UTF_8);
        byte[] out = new byte[2 + idBytes.length + 8 + len];
        out[0] = (byte) type;
        out[1] = (byte) idBytes.length;
        System.arraycopy(idBytes, 0, out, 2, idBytes.length);
        int pos = 2 + idBytes.length;
        for (int shift = 56; shift >= 0; shift -= 8) out[pos++] = (byte) (offset >>> shift);
        System.arraycopy(data, 0, out, pos, len);
        return out;
    }
}
