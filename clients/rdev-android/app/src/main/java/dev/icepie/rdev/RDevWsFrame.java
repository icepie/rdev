package dev.icepie.rdev;

import java.io.ByteArrayOutputStream;

final class RDevWsFrame {
    final int opcode;
    final byte[] payload;

    private RDevWsFrame(int opcode, byte[] payload) {
        this.opcode = opcode;
        this.payload = payload;
    }

    static byte[] encodeText(String text) {
        try { return encode(1, text.getBytes("UTF-8")); }
        catch (Exception e) { return encode(1, text.getBytes()); }
    }

    static byte[] encode(int opcode, byte[] payload) {
        ByteArrayOutputStream out = new ByteArrayOutputStream();
        out.write(0x80 | (opcode & 0x0f));
        int len = payload.length;
        if (len < 126) {
            out.write(len);
        } else if (len <= 0xffff) {
            out.write(126);
            out.write((len >>> 8) & 0xff);
            out.write(len & 0xff);
        } else {
            out.write(127);
            long v = len;
            for (int i = 7; i >= 0; i--) out.write((int) ((v >>> (8 * i)) & 0xff));
        }
        out.write(payload, 0, payload.length);
        return out.toByteArray();
    }

    static RDevWsFrame decode(byte[] data) {
        if (data == null || data.length < 2) return null;
        int pos = 0;
        int b0 = data[pos++] & 0xff;
        int b1 = data[pos++] & 0xff;
        int opcode = b0 & 0x0f;
        long len = b1 & 0x7f;
        if (len == 126) {
            if (data.length < pos + 2) return null;
            len = ((data[pos++] & 0xffL) << 8) | (data[pos++] & 0xffL);
        } else if (len == 127) {
            if (data.length < pos + 8) return null;
            len = 0;
            for (int i = 0; i < 8; i++) len = (len << 8) | (data[pos++] & 0xffL);
        }
        byte[] mask = null;
        if ((b1 & 0x80) != 0) {
            if (data.length < pos + 4) return null;
            mask = new byte[] {data[pos++], data[pos++], data[pos++], data[pos++]};
        }
        if (len > Integer.MAX_VALUE || data.length < pos + (int) len) return null;
        byte[] payload = new byte[(int) len];
        System.arraycopy(data, pos, payload, 0, payload.length);
        if (mask != null) {
            for (int i = 0; i < payload.length; i++) payload[i] ^= mask[i & 3];
        }
        return new RDevWsFrame(opcode, payload);
    }
}
