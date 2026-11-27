package dev.icepie.rdev;

import java.io.ByteArrayInputStream;
import java.io.EOFException;
import java.io.File;
import java.io.IOException;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.util.ArrayList;
import java.util.Arrays;
import java.util.List;

/**
 * Shared-byte-corpus test. The same files under
 * {@code internal/transport/testdata/framing} are decoded by the Go server
 * ({@code internal/transport/framing_fixture_test.go}) and the Rust client
 * ({@code clients/rdev-client-rs/tests/framing_fixtures.rs});
 * {@code docs/protocol-framing.md} describes the layout.
 *
 * <p>Runs under the Android unit tests and stand-alone with
 * {@code javac} + {@code java dev.icepie.rdev.FramingFixtures} when no Android
 * SDK is installed.
 */
final class FramingFixtures {
    private FramingFixtures() {}

    static File fixtureRoot() {
        String configured = System.getProperty("rdev.fixture.root");
        if (configured == null || configured.isEmpty()) configured = System.getenv("RDEV_FIXTURE_ROOT");
        if (configured != null && !configured.isEmpty()) return new File(configured);
        File dir = new File(System.getProperty("user.dir")).getAbsoluteFile();
        while (dir != null) {
            File candidate = new File(dir, "internal/transport/testdata/framing");
            if (new File(candidate, "fixtures.tsv").isFile()) return candidate;
            dir = dir.getParentFile();
        }
        throw new IllegalStateException(
                "fixtures.tsv not found; set -Drdev.fixture.root or RDEV_FIXTURE_ROOT");
    }

    static void checkStreamFrames() throws IOException {
        List<String[]> rows = loadRows("stream");
        int index = 0;
        while (index < rows.size()) {
            String file = rows.get(index)[1];
            byte[] data = Files.readAllBytes(new File(fixtureRoot(), file).toPath());
            ByteArrayInputStream in = new ByteArrayInputStream(data);
            while (index < rows.size() && rows.get(index)[1].equals(file)) {
                String[] row = rows.get(index);
                String name = row[0];
                boolean last = index + 1 == rows.size() || !rows.get(index + 1)[1].equals(file);
                byte[] remaining = last ? hex(row[5]) : null;
                if (!row[2].equals("ok")) {
                    try {
                        RDevStreamFrame.read(in);
                        throw new AssertionError(name + " (" + file + "): expected decode failure");
                    } catch (IOException expected) {
                        String want = row[2].substring("error:".length());
                        String got = classifyStream(expected);
                        if (!got.equals(want))
                            throw new AssertionError(
                                    name + " (" + file + "): " + expected + " classified as " + got
                                            + ", want " + want);
                    }
                    index++;
                    continue;
                }
                RDevStreamFrame frame = RDevStreamFrame.read(in);
                int kind = Integer.parseInt(row[3]);
                if (frame.kind != kind)
                    throw new AssertionError(
                            name + " (" + file + "): kind=" + frame.kind + ", want " + kind);
                byte[] payload = hex(row[4]);
                if (!Arrays.equals(frame.payload, payload))
                    throw new AssertionError(
                            name + " (" + file + "): payload " + bytes(frame.payload) + ", want "
                                    + bytes(payload));
                if (last) {
                    byte[] left = readAll(in);
                    if (!Arrays.equals(left, remaining))
                        throw new AssertionError(
                                name + " (" + file + "): " + left.length + " trailing bytes "
                                        + bytes(left) + ", want " + bytes(remaining));
                }
                index++;
            }
        }
    }

    static void checkBinaryFrames() throws IOException {
        List<String[]> rows = loadRows("inner");
        for (String[] row : rows) {
            String name = row[0];
            String file = row[1];
            byte[] raw = Files.readAllBytes(new File(fixtureRoot(), file).toPath());
            if (!row[2].equals("ok")) {
                try {
                    RDevProtocol.decode(raw);
                    throw new AssertionError(name + " (" + file + "): decoder accepted an invalid frame");
                } catch (Exception expected) {
                    // rejected as required
                }
                continue;
            }
            int type = Integer.parseInt(row[3]);
            String id = row[4];
            boolean offseted = row[5].equals("1");
            byte[] payload = hex(row[7]);
            RDevProtocol frame;
            try {
                frame = RDevProtocol.decode(raw);
            } catch (Exception error) {
                throw new AssertionError(name + " (" + file + "): " + error, error);
            }
            if (frame.type != type)
                throw new AssertionError(name + " (" + file + "): type=" + frame.type + ", want " + type);
            if (!frame.id.equals(id))
                throw new AssertionError(name + " (" + file + "): id=" + frame.id + ", want " + id);
            if (offseted) {
                long offset = Long.parseLong(row[6]);
                if (!RDevProtocol.isOffsetType(type))
                    throw new AssertionError(name + " (" + file + "): type not treated as offset frame");
                if (frame.offset != offset)
                    throw new AssertionError(
                            name + " (" + file + "): offset=" + frame.offset + ", want " + offset);
                if (!Arrays.equals(frame.offsetPayload, payload))
                    throw new AssertionError(
                            name + " (" + file + "): offset payload " + bytes(frame.offsetPayload)
                                    + ", want " + bytes(payload));
                byte[] encoded;
                try {
                    encoded = RDevProtocol.encodeOffset(type, id, offset, payload, payload.length);
                } catch (Exception error) {
                    throw new AssertionError(name + " (" + file + "): " + error, error);
                }
                if (!Arrays.equals(encoded, raw))
                    throw new AssertionError(name + " (" + file + "): fixture bytes differ from encodeOffset");
            } else {
                if (RDevProtocol.isOffsetType(type))
                    throw new AssertionError(name + " (" + file + "): type treated as offset frame");
                if (!Arrays.equals(frame.payload, payload))
                    throw new AssertionError(
                            name + " (" + file + "): payload " + bytes(frame.payload) + ", want "
                                    + bytes(payload));
                byte[] encoded;
                try {
                    encoded = RDevProtocol.encode(type, id, payload, payload.length);
                } catch (Exception error) {
                    throw new AssertionError(name + " (" + file + "): " + error, error);
                }
                if (!Arrays.equals(encoded, raw))
                    throw new AssertionError(name + " (" + file + "): fixture bytes differ from encode");
            }
        }
    }

    /** 64 KiB and 512 KiB round trips, in order, chunked at 16 KiB. */
    static void checkLargePayloadRoundTrips() throws Exception {
        for (int size : new int[] {64 * 1024, 512 * 1024}) {
            byte[] payload = new byte[size];
            for (int i = 0; i < size; i++) payload[i] = (byte) ((i * 31) % 251);
            byte[] plain = RDevProtocol.encode(RDevProtocol.BIN_DATA, "session-large", payload, payload.length);
            RDevProtocol decoded = RDevProtocol.decode(plain);
            if (decoded.type != RDevProtocol.BIN_DATA || !decoded.id.equals("session-large"))
                throw new AssertionError("plain " + size + ": header " + decoded.type + "/" + decoded.id);
            if (!Arrays.equals(decoded.payload, payload))
                throw new AssertionError("plain " + size + ": payload mismatch");

            long offset = 0;
            byte[] rebuilt = new byte[size];
            for (int start = 0; start < size; start += 16 * 1024) {
                int len = Math.min(16 * 1024, size - start);
                byte[] chunk = new byte[len];
                System.arraycopy(payload, start, chunk, 0, len);
                byte[] frame =
                        RDevProtocol.encodeOffset(
                                RDevProtocol.BIN_FILE_UPLOAD_CHUNK, "task-large", offset, chunk, len);
                RDevProtocol round = RDevProtocol.decode(frame);
                if (round.type != RDevProtocol.BIN_FILE_UPLOAD_CHUNK || !round.id.equals("task-large"))
                    throw new AssertionError("offset " + size + ": header " + round.type + "/" + round.id);
                if (round.offset != offset)
                    throw new AssertionError(
                            "offset " + size + ": offset=" + round.offset + ", want " + offset);
                if (!Arrays.equals(round.offsetPayload, chunk))
                    throw new AssertionError("offset " + size + ": chunk mismatch at " + offset);
                System.arraycopy(round.offsetPayload, 0, rebuilt, start, len);
                offset += len;
            }
            if (offset != size) throw new AssertionError("offset " + size + ": bytes=" + offset);
            if (!Arrays.equals(rebuilt, payload))
                throw new AssertionError("offset " + size + ": reassembled payload mismatch");
        }
    }

    public static void main(String[] args) throws Exception {
        checkStreamFrames();
        checkBinaryFrames();
        checkLargePayloadRoundTrips();
        System.out.println("framing fixtures ok: " + fixtureRoot().getCanonicalPath());
    }

    private static List<String[]> loadRows(String section) throws IOException {
        byte[] text = Files.readAllBytes(new File(fixtureRoot(), "fixtures.tsv").toPath());
        List<String[]> rows = new ArrayList<>();
        String current = "";
        for (String line : new String(text, StandardCharsets.UTF_8).split("\n")) {
            if (line.isEmpty() || line.startsWith("#")) continue;
            if (line.startsWith("[") && line.endsWith("]")) {
                current = line.substring(1, line.length() - 1);
                continue;
            }
            if (!current.equals(section)) continue;
            String[] fields = line.split("\t", -1);
            int want = section.equals("stream") ? 6 : 8;
            if (fields.length != want)
                throw new AssertionError("row " + line + ": " + fields.length + " fields, want " + want);
            rows.add(fields);
        }
        if (rows.isEmpty()) throw new AssertionError("no rows for section " + section);
        return rows;
    }

    private static String classifyStream(IOException error) {
        if (error instanceof EOFException) return "eof";
        if (String.valueOf(error.getMessage()).contains("too large")) return "too_large";
        return "other:" + error.getMessage();
    }

    private static byte[] readAll(ByteArrayInputStream in) {
        byte[] data = new byte[in.available()];
        int off = 0;
        while (off < data.length) {
            int n = in.read(data, off, data.length - off);
            if (n < 0) break;
            off += n;
        }
        return off == data.length ? data : Arrays.copyOf(data, off);
    }

    private static byte[] hex(String text) {
        if (text.isEmpty()) return new byte[0];
        if (text.length() % 2 != 0) throw new AssertionError("odd hex: " + text);
        byte[] out = new byte[text.length() / 2];
        for (int i = 0; i < out.length; i++)
            out[i] = (byte) Integer.parseInt(text.substring(i * 2, i * 2 + 2), 16);
        return out;
    }

    private static String bytes(byte[] data) {
        StringBuilder sb = new StringBuilder(data.length * 2 + 2);
        sb.append('[');
        for (byte b : data) {
            sb.append(String.format("%02x", b & 0xff));
        }
        sb.append(']');
        return sb.toString();
    }
}
