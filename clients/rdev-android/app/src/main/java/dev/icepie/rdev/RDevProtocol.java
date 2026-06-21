package dev.icepie.rdev;

import java.io.ByteArrayOutputStream;
import java.nio.ByteBuffer;

final class RDevProtocol {
    static final int BIN_DATA = 0x01;
    static final int BIN_STDERR = 0x02;
    static final int BIN_FILE_UPLOAD_CHUNK = 0x20;
    static final int BIN_FILE_UPLOAD_ACK = 0x21;
    static final int BIN_FILE_DOWNLOAD_CHUNK = 0x22;
    static final int BIN_FILE_TRANSFER_END = 0x23;
    static final int BIN_FILE_TRANSFER_CANCEL = 0x24;

    final int type;
    final String id;
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

    static RDevProtocol decode(byte[] raw) throws Exception {
        if (raw == null || raw.length < 2) throw new IllegalArgumentException("binary frame too short");
        int type = raw[0] & 0xff;
        int idLen = raw[1] & 0xff;
        if (raw.length < 2 + idLen) throw new IllegalArgumentException("binary frame id truncated");
        String id = new String(raw, 2, idLen, "UTF-8");
        byte[] payload = new byte[raw.length - 2 - idLen];
        System.arraycopy(raw, 2 + idLen, payload, 0, payload.length);
        long offset = 0;
        byte[] offsetPayload = payload;
        if (payload.length >= 8) {
            offset = ByteBuffer.wrap(payload, 0, 8).getLong();
            offsetPayload = new byte[payload.length - 8];
            System.arraycopy(payload, 8, offsetPayload, 0, offsetPayload.length);
        }
        return new RDevProtocol(type, id, payload, offset, offsetPayload);
    }

    static byte[] encode(int type, String id, byte[] data, int len) throws Exception {
        byte[] idBytes = id.getBytes("UTF-8");
        ByteArrayOutputStream out = new ByteArrayOutputStream(2 + idBytes.length + len);
        out.write(type & 0xff);
        out.write(idBytes.length & 0xff);
        out.write(idBytes);
        out.write(data, 0, len);
        return out.toByteArray();
    }

    static byte[] encodeOffset(int type, String id, long offset, byte[] data, int len) throws Exception {
        byte[] idBytes = id.getBytes("UTF-8");
        ByteArrayOutputStream out = new ByteArrayOutputStream(2 + idBytes.length + 8 + len);
        out.write(type & 0xff);
        out.write(idBytes.length & 0xff);
        out.write(idBytes);
        for (int i = 7; i >= 0; i--) out.write((int) ((offset >>> (8 * i)) & 0xff));
        out.write(data, 0, len);
        return out.toByteArray();
    }
}
