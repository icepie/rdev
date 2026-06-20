package cn.singzer.rdev.android;

import java.io.ByteArrayOutputStream;
import java.io.IOException;

final class Fmp4Muxer {
    private final int width;
    private final int height;
    private final byte[] sps;
    private final byte[] pps;
    private int sequence = 1;
    private long baseTime;
    private long lastPtsUs = -1;

    Fmp4Muxer(int width, int height, byte[] sps, byte[] pps) {
        this.width = width;
        this.height = height;
        this.sps = stripStartCode(sps);
        this.pps = stripStartCode(pps);
    }

    byte[] initSegment() throws IOException {
        return concat(ftyp(), moov());
    }

    byte[] fragment(byte[] sample, long ptsUs, boolean keyFrame) throws IOException {
        byte[] payload = avccSample(sample);
        long time = ptsUs * 90 / 1000;
        if (baseTime == 0) baseTime = time;
        int duration = lastPtsUs < 0 ? 3000 : Math.max(1, (int) ((ptsUs - lastPtsUs) * 90 / 1000));
        lastPtsUs = ptsUs;
        byte[] moof = moof(sequence++, time - baseTime, duration, payload.length, keyFrame);
        byte[] mdat = box("mdat", payload);
        return concat(moof, mdat);
    }

    private byte[] ftyp() throws IOException {
        ByteArrayOutputStream out = new ByteArrayOutputStream();
        out.write(ascii("isom"));
        u32(out, 0x200);
        out.write(ascii("isom"));
        out.write(ascii("iso6"));
        out.write(ascii("avc1"));
        out.write(ascii("mp41"));
        return box("ftyp", out.toByteArray());
    }

    private byte[] moov() throws IOException {
        return box("moov", concat(mvhd(), trak(), mvex()));
    }

    private byte[] mvhd() throws IOException {
        ByteArrayOutputStream out = new ByteArrayOutputStream();
        u32(out, 0); u32(out, 0); u32(out, 0); u32(out, 90000); u32(out, 0);
        u32(out, 0x00010000); u16(out, 0x0100); u16(out, 0); u32(out, 0); u32(out, 0);
        int[] matrix = {0x00010000,0,0,0,0x00010000,0,0,0,0x40000000};
        for (int v : matrix) u32(out, v);
        for (int i = 0; i < 6; i++) u32(out, 0);
        u32(out, 2);
        return box("mvhd", out.toByteArray());
    }

    private byte[] trak() throws IOException {
        return box("trak", concat(tkhd(), mdia()));
    }

    private byte[] tkhd() throws IOException {
        ByteArrayOutputStream out = new ByteArrayOutputStream();
        u32(out, 0x00000007); u32(out, 0); u32(out, 0); u32(out, 1); u32(out, 0); u32(out, 0);
        u32(out, 0); u32(out, 0); u16(out, 0); u16(out, 0); u16(out, 0); u16(out, 0);
        int[] matrix = {0x00010000,0,0,0,0x00010000,0,0,0,0x40000000};
        for (int v : matrix) u32(out, v);
        u32(out, width << 16); u32(out, height << 16);
        return box("tkhd", out.toByteArray());
    }

    private byte[] mdia() throws IOException { return box("mdia", concat(mdhd(), hdlr(), minf())); }

    private byte[] mdhd() throws IOException {
        ByteArrayOutputStream out = new ByteArrayOutputStream();
        u32(out, 0); u32(out, 0); u32(out, 0); u32(out, 90000); u32(out, 0); u16(out, 0x55c4); u16(out, 0);
        return box("mdhd", out.toByteArray());
    }

    private byte[] hdlr() throws IOException {
        ByteArrayOutputStream out = new ByteArrayOutputStream();
        u32(out, 0); u32(out, 0); out.write(ascii("vide")); u32(out, 0); u32(out, 0); u32(out, 0); out.write(0);
        return box("hdlr", out.toByteArray());
    }

    private byte[] minf() throws IOException { return box("minf", concat(vmhd(), dinf(), stbl())); }

    private byte[] vmhd() throws IOException { ByteArrayOutputStream out = new ByteArrayOutputStream(); u32(out, 1); u16(out, 0); u16(out, 0); u16(out, 0); u16(out, 0); return box("vmhd", out.toByteArray()); }
    private byte[] dinf() throws IOException { return box("dinf", box("dref", concat(u32bytes(0), u32bytes(1), box("url ", u32bytes(1))))); }
    private byte[] stbl() throws IOException { return box("stbl", concat(stsd(), box("stts", concat(u32bytes(0), u32bytes(0))), box("stsc", concat(u32bytes(0), u32bytes(0))), box("stsz", concat(u32bytes(0), u32bytes(0), u32bytes(0))), box("stco", concat(u32bytes(0), u32bytes(0))))); }

    private byte[] stsd() throws IOException {
        return box("stsd", concat(u32bytes(0), u32bytes(1), avc1()));
    }

    private byte[] avc1() throws IOException {
        ByteArrayOutputStream out = new ByteArrayOutputStream();
        for (int i = 0; i < 6; i++) out.write(0);
        u16(out, 1); u16(out, 0); u16(out, 0); u32(out, 0); u32(out, 0); u32(out, 0);
        u16(out, width); u16(out, height); u32(out, 0x00480000); u32(out, 0x00480000); u32(out, 0); u16(out, 1);
        out.write(0); for (int i = 0; i < 31; i++) out.write(0);
        u16(out, 0x18); u16(out, 0xffff);
        out.write(avcC());
        return box("avc1", out.toByteArray());
    }

    private byte[] avcC() throws IOException {
        ByteArrayOutputStream out = new ByteArrayOutputStream();
        out.write(1);
        out.write(sps.length > 3 ? sps[1] & 0xff : 0x42);
        out.write(sps.length > 3 ? sps[2] & 0xff : 0x00);
        out.write(sps.length > 3 ? sps[3] & 0xff : 0x1f);
        out.write(0xff);
        out.write(0xe1); u16(out, sps.length); out.write(sps);
        out.write(1); u16(out, pps.length); out.write(pps);
        return box("avcC", out.toByteArray());
    }

    private byte[] mvex() throws IOException { return box("mvex", trex()); }
    private byte[] trex() throws IOException { ByteArrayOutputStream out = new ByteArrayOutputStream(); u32(out, 0); u32(out, 1); u32(out, 1); u32(out, 0); u32(out, 0); u32(out, 0); return box("trex", out.toByteArray()); }

    private byte[] moof(int seq, long time, int duration, int size, boolean keyFrame) throws IOException {
        byte[] mfhd = mfhd(seq);
        byte[] trafNoOffset = traf(time, duration, size, keyFrame, 0);
        int dataOffset = 8 + mfhd.length + trafNoOffset.length + 8;
        return box("moof", concat(mfhd, traf(time, duration, size, keyFrame, dataOffset)));
    }

    private byte[] mfhd(int seq) throws IOException { return box("mfhd", concat(u32bytes(0), u32bytes(seq))); }

    private byte[] traf(long time, int duration, int size, boolean keyFrame, int dataOffset) throws IOException {
        return box("traf", concat(tfhd(), tfdt(time), trun(duration, size, keyFrame, dataOffset)));
    }

    private byte[] tfhd() throws IOException { return box("tfhd", concat(u32bytes(0x020000), u32bytes(1))); }
    private byte[] tfdt(long time) throws IOException { ByteArrayOutputStream out = new ByteArrayOutputStream(); u32(out, 1 << 24); u64(out, time); return box("tfdt", out.toByteArray()); }

    private byte[] trun(int duration, int size, boolean keyFrame, int dataOffset) throws IOException {
        ByteArrayOutputStream out = new ByteArrayOutputStream();
        u32(out, 0x000f01); u32(out, 1); u32(out, dataOffset); u32(out, duration); u32(out, size); u32(out, keyFrame ? 0x02000000 : 0x01010000);
        return box("trun", out.toByteArray());
    }

    private byte[] avccSample(byte[] sample) throws IOException {
        byte[] raw = stripStartCode(sample);
        ByteArrayOutputStream out = new ByteArrayOutputStream();
        int off = 0;
        while (off < raw.length) {
            int next = findStartCode(raw, off);
            if (next < 0) {
                u32(out, raw.length - off); out.write(raw, off, raw.length - off); break;
            }
            if (next > off) { u32(out, next - off); out.write(raw, off, next - off); }
            off = skipStartCode(raw, next);
        }
        byte[] converted = out.toByteArray();
        if (converted.length == 0) { u32(out, raw.length); out.write(raw); converted = out.toByteArray(); }
        return converted;
    }

    private static byte[] stripStartCode(byte[] data) {
        if (data == null) return new byte[0];
        int off = skipStartCode(data, 0);
        byte[] out = new byte[data.length - off];
        System.arraycopy(data, off, out, 0, out.length);
        return out;
    }

    private static int findStartCode(byte[] data, int from) {
        for (int i = from; i + 3 < data.length; i++) {
            if (data[i] == 0 && data[i + 1] == 0 && data[i + 2] == 1) return i;
            if (i + 4 < data.length && data[i] == 0 && data[i + 1] == 0 && data[i + 2] == 0 && data[i + 3] == 1) return i;
        }
        return -1;
    }

    private static int skipStartCode(byte[] data, int off) {
        if (data.length >= off + 4 && data[off] == 0 && data[off + 1] == 0 && data[off + 2] == 0 && data[off + 3] == 1) return off + 4;
        if (data.length >= off + 3 && data[off] == 0 && data[off + 1] == 0 && data[off + 2] == 1) return off + 3;
        return off;
    }

    private static byte[] box(String type, byte[] payload) throws IOException { ByteArrayOutputStream out = new ByteArrayOutputStream(); u32(out, 8 + payload.length); out.write(ascii(type)); out.write(payload); return out.toByteArray(); }
    private static byte[] concat(byte[]... parts) throws IOException { ByteArrayOutputStream out = new ByteArrayOutputStream(); for (byte[] p : parts) out.write(p); return out.toByteArray(); }
    private static byte[] ascii(String value) { try { return value.getBytes("US-ASCII"); } catch (Exception e) { return value.getBytes(); } }
    private static byte[] u32bytes(int v) throws IOException { ByteArrayOutputStream out = new ByteArrayOutputStream(); u32(out, v); return out.toByteArray(); }
    private static void u16(ByteArrayOutputStream out, int v) { out.write((v >>> 8) & 0xff); out.write(v & 0xff); }
    private static void u32(ByteArrayOutputStream out, long v) { out.write((int) ((v >>> 24) & 0xff)); out.write((int) ((v >>> 16) & 0xff)); out.write((int) ((v >>> 8) & 0xff)); out.write((int) (v & 0xff)); }
    private static void u64(ByteArrayOutputStream out, long v) { for (int i = 7; i >= 0; i--) out.write((int) ((v >>> (8 * i)) & 0xff)); }
}
