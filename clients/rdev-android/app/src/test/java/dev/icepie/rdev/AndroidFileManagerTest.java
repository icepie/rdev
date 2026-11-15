package dev.icepie.rdev;

import org.junit.Test;

import java.io.File;
import java.io.FileOutputStream;
import java.nio.charset.StandardCharsets;
import java.security.MessageDigest;

import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertFalse;
import static org.junit.Assert.assertTrue;

public class AndroidFileManagerTest {
    @Test public void uploadRequiresLowercaseFullSha256() {
        assertTrue(AndroidFileManager.validSha256("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"));
        assertFalse(AndroidFileManager.validSha256("0123456789ABCDEF0123456789abcdef0123456789abcdef0123456789abcdef"));
        assertFalse(AndroidFileManager.validSha256("short"));
    }

    @Test public void partialResumesOnlyForExactSidecarIdentity() {
        String digest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef";
        assertTrue(AndroidFileManager.sidecarMatches(digest, "42", null, digest, 42));
        assertFalse(AndroidFileManager.sidecarMatches(digest, "43", null, digest, 42));
        assertFalse(AndroidFileManager.sidecarMatches("different", "42", null, digest, 42));
        assertFalse(AndroidFileManager.sidecarMatches(digest, "42", "unexpected", digest, 42));
    }

    @Test public void uploadChunksCannotCrossDeclaredSizeOrChunkBound() {
        assertTrue(AndroidFileManager.validUploadChunk(0, 16 * 1024, 16 * 1024));
        assertFalse(AndroidFileManager.validUploadChunk(1, 16 * 1024, 16 * 1024));
        assertFalse(AndroidFileManager.validUploadChunk(0, 16 * 1024 + 1, 16 * 1024 + 1));
        assertFalse(AndroidFileManager.validUploadChunk(-1, 1, 1));
    }

    @Test public void staleSameSizePartialFailsFinalHashAndClearsBinding() throws Exception {
        File dir = tempDir();
        File part = new File(dir, "target.rdevpart");
        File meta = new File(part.getPath() + ".meta");
        String expected = sha256("bbbbbbbb");
        write(part, "OLD!");
        write(meta, expected + "\n8\n");
        AndroidFileManager manager = new AndroidFileManager(null);
        assertEquals(4, manager.prepareUploadPartial(part, meta, expected, 8, false));
        write(part, "OLD!bbbb");
        try {
            manager.publishUploadPart(part, new File(dir, "target"), meta, 8, expected, (from, to) -> { });
            throw new AssertionError("forged partial unexpectedly published");
        } catch (Exception expectedFailure) {
            assertFalse(meta.exists());
            assertTrue(part.exists());
        }
        delete(dir);
    }

    @Test public void failedAtomicReplacementPreservesOriginalTarget() throws Exception {
        File dir = tempDir();
        File target = new File(dir, "target");
        File part = new File(dir, "target.rdevpart");
        File meta = new File(part.getPath() + ".meta");
        String digest = sha256("bbbbbbbb");
        write(target, "original");
        write(part, "bbbbbbbb");
        write(meta, digest + "\n8\n");
        AndroidFileManager manager = new AndroidFileManager(null);
        try {
            manager.publishUploadPart(part, target, meta, 8, digest, (from, to) -> { throw new java.io.IOException("injected rename failure"); });
            throw new AssertionError("replacement unexpectedly succeeded");
        } catch (Exception expectedFailure) {
            assertEquals("original", read(target));
            assertTrue(part.exists());
            assertTrue(meta.exists());
        }
        delete(dir);
    }

    private static String sha256(String value) throws Exception {
        byte[] digest = MessageDigest.getInstance("SHA-256").digest(value.getBytes(StandardCharsets.UTF_8));
        StringBuilder out = new StringBuilder();
        for (byte b : digest) out.append(String.format("%02x", b & 0xff));
        return out.toString();
    }

    private static File tempDir() throws Exception {
        File dir = File.createTempFile("rdev-android", "test");
        if (!dir.delete() || !dir.mkdirs()) throw new java.io.IOException("temp directory setup failed");
        return dir;
    }

    private static void write(File file, String text) throws Exception {
        try (FileOutputStream out = new FileOutputStream(file)) { out.write(text.getBytes(StandardCharsets.UTF_8)); }
    }

    private static String read(File file) throws Exception {
        return new String(java.nio.file.Files.readAllBytes(file.toPath()), StandardCharsets.UTF_8);
    }

    private static void delete(File file) {
        File[] children = file.listFiles();
        if (children != null) for (File child : children) delete(child);
        file.delete();
    }
}
