package dev.icepie.rdev;

import org.junit.Test;

import java.io.File;
import java.io.FileOutputStream;
import java.io.IOException;
import java.nio.charset.StandardCharsets;
import java.security.MessageDigest;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;
import java.util.concurrent.Future;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.TimeoutException;

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

    @Test public void uploadDispatchIsOrderedPerTaskButIndependentAcrossTasks() throws Exception {
        AndroidFileDispatcher dispatcher = new AndroidFileDispatcher();
        CountDownLatch entered = new CountDownLatch(1);
        CountDownLatch release = new CountDownLatch(1);
        CountDownLatch independent = new CountDownLatch(1);
        CountDownLatch finished = new CountDownLatch(1);
        StringBuilder order = new StringBuilder();
        try {
            dispatcher.execute("first", () -> {
                entered.countDown();
                try { await(release); } catch (IOException e) { throw new AssertionError(e); }
                order.append("start,");
            });
            assertTrue(entered.await(5, TimeUnit.SECONDS));
            dispatcher.execute("first", () -> order.append("chunk,"));
            dispatcher.execute("first", () -> order.append("end,"));
            dispatcher.execute("first", () -> { order.append("cancel"); finished.countDown(); });
            dispatcher.execute("second", independent::countDown);
            assertTrue(independent.await(5, TimeUnit.SECONDS));
            assertEquals(1, finished.getCount());
            release.countDown();
            assertTrue(finished.await(5, TimeUnit.SECONDS));
            assertEquals("start,chunk,end,cancel", order.toString());
        } finally {
            release.countDown();
            dispatcher.close();
        }
    }

    @Test public void dispatchByteBudgetBackpressuresUntilCompletion() throws Exception {
        AndroidFileDispatcher dispatcher = new AndroidFileDispatcher();
        ExecutorService producer = Executors.newSingleThreadExecutor();
        CountDownLatch entered = new CountDownLatch(1);
        CountDownLatch release = new CountDownLatch(1);
        CountDownLatch submitting = new CountDownLatch(1);
        CountDownLatch executed = new CountDownLatch(1);
        CountDownLatch independent = new CountDownLatch(1);
        try {
            dispatcher.execute("full", AndroidFileDispatcher.MAX_PENDING_BYTES, () -> {
                entered.countDown();
                try { await(release); } catch (IOException e) { throw new AssertionError(e); }
            });
            assertTrue(entered.await(5, TimeUnit.SECONDS));
            Future<?> admission = producer.submit(() -> {
                submitting.countDown();
                dispatcher.execute("next", 1, executed::countDown);
            });
            assertTrue(submitting.await(5, TimeUnit.SECONDS));
            assertBlocked(admission);
            dispatcher.execute("control", independent::countDown);
            assertTrue(independent.await(5, TimeUnit.SECONDS));
            assertEquals(1, executed.getCount());
            release.countDown();
            admission.get(5, TimeUnit.SECONDS);
            assertTrue(executed.await(5, TimeUnit.SECONDS));
        } finally {
            release.countDown();
            dispatcher.close();
            producer.shutdownNow();
        }
    }

    @Test public void dispatchCloseWakesOperationLimitedAdmission() throws Exception {
        assertCloseWakesAdmission(false);
    }

    @Test public void dispatchCloseWakesByteLimitedAdmission() throws Exception {
        assertCloseWakesAdmission(true);
    }

    private static void assertCloseWakesAdmission(boolean byteLimited) throws Exception {
        AndroidFileDispatcher dispatcher = new AndroidFileDispatcher();
        ExecutorService producer = Executors.newSingleThreadExecutor();
        CountDownLatch entered = new CountDownLatch(1);
        CountDownLatch release = new CountDownLatch(1);
        CountDownLatch submitting = new CountDownLatch(1);
        java.util.concurrent.atomic.AtomicBoolean queuedRan = new java.util.concurrent.atomic.AtomicBoolean();
        try {
            dispatcher.execute("full", byteLimited ? AndroidFileDispatcher.MAX_PENDING_BYTES : 0, () -> {
                entered.countDown();
                try { await(release); } catch (IOException e) { throw new AssertionError(e); }
            });
            assertTrue(entered.await(5, TimeUnit.SECONDS));
            if (!byteLimited) {
                for (int i = 1; i < AndroidFileDispatcher.MAX_PENDING_OPERATIONS; i++)
                    dispatcher.execute("full", () -> queuedRan.set(true));
            }
            Future<?> admission = producer.submit(() -> {
                submitting.countDown();
                dispatcher.execute("overflow", byteLimited ? 1 : 0, () -> queuedRan.set(true));
            });
            assertTrue(submitting.await(5, TimeUnit.SECONDS));
            assertBlocked(admission);
            dispatcher.close();
            admission.get(5, TimeUnit.SECONDS);
            dispatcher.execute("after-close", () -> queuedRan.set(true));
            release.countDown();
            assertFalse(queuedRan.get());
        } finally {
            release.countDown();
            dispatcher.close();
            producer.shutdownNow();
        }
    }

    @Test public void dispatchWorkerConcurrencyIsBounded() throws Exception {
        AndroidFileDispatcher dispatcher = new AndroidFileDispatcher();
        CountDownLatch entered = new CountDownLatch(AndroidFileDispatcher.WORKER_COUNT);
        CountDownLatch release = new CountDownLatch(1);
        CountDownLatch overflow = new CountDownLatch(1);
        try {
            for (int i = 0; i < AndroidFileDispatcher.WORKER_COUNT; i++) {
                dispatcher.execute("worker-" + i, () -> {
                    entered.countDown();
                    try { await(release); } catch (IOException e) { throw new AssertionError(e); }
                });
            }
            assertTrue(entered.await(5, TimeUnit.SECONDS));
            dispatcher.execute("overflow", overflow::countDown);
            assertFalse(overflow.await(100, TimeUnit.MILLISECONDS));
            release.countDown();
            assertTrue(overflow.await(5, TimeUnit.SECONDS));
        } finally {
            release.countDown();
            dispatcher.close();
        }
    }

    private static void assertBlocked(Future<?> operation) throws Exception {
        try {
            operation.get(100, TimeUnit.MILLISECONDS);
            throw new AssertionError("admission did not apply backpressure");
        } catch (TimeoutException expected) { }
    }

    @Test public void slowCancelRetainsLeaseWithoutBlockingOtherTargets() throws Exception {
        File dir = tempDir();
        AndroidFileManager manager = new AndroidFileManager(null);
        ExecutorService workers = Executors.newCachedThreadPool();
        CountDownLatch closing = new CountDownLatch(1);
        CountDownLatch release = new CountDownLatch(1);
        try {
            AndroidFileManager.Upload upload = readyUpload(manager, dir, "first", "ab", "abcd");
            upload.out.close();
            upload.out = new FileOutputStream(upload.part, true) {
                @Override public void close() throws IOException {
                    closing.countDown();
                    try { await(release); } finally { super.close(); }
                }
            };
            Future<?> cancelled = workers.submit(() -> manager.cancel("first"));
            assertTrue(closing.await(5, TimeUnit.SECONDS));
            workers.submit(() -> {
                expectIOException(() -> manager.reserveUpload("second", upload.target.getPath(), 4, upload.sha256));
                AndroidFileManager.Upload independent = readyUpload(manager, dir, "independent", "x", "x");
                manager.finishUpload(independent, AndroidFileManagerTest::replace);
                return null;
            }).get(5, TimeUnit.SECONDS);
            assertFalse(cancelled.isDone());
            release.countDown();
            cancelled.get(5, TimeUnit.SECONDS);
            AndroidFileManager.Upload resumed = manager.reserveUpload("second", upload.target.getPath(), 4, upload.sha256);
            assertEquals(sha256("ab"), manager.initializeUpload(resumed, false));
            expectIOException(() -> manager.writeUpload(upload, 2, new byte[] { 1 }));
            expectIOException(() -> manager.finishUpload(upload, AndroidFileManagerTest::replace));
        } finally {
            release.countDown();
            workers.shutdownNow();
            assertTrue(workers.awaitTermination(5, TimeUnit.SECONDS));
            manager.closeAll();
            delete(dir);
        }
    }

    @Test public void destinationLeaseCoversAliasesAndInitialization() throws Exception {
        File dir = tempDir();
        AndroidFileManager manager = new AndroidFileManager(null);
        try {
            File real = new File(dir, "real");
            assertTrue(real.mkdir());
            File alias = new File(dir, "alias");
            java.nio.file.Files.createSymbolicLink(alias.toPath(), real.toPath());
            String digest = sha256("payload");
            File target = new File(real, "target");
            AndroidFileManager.Upload upload = manager.reserveUpload("first", target.getPath(), 7, digest);
            expectIOException(() -> manager.reserveUpload("second", new File(alias, "./target").getPath(), 7, digest));
            expectIOException(() -> manager.reserveUpload("first", new File(real, "other").getPath(), 7, digest));
            assertFalse(upload.part.exists());
            manager.cancel("first");
            AndroidFileManager.Upload replacement = manager.reserveUpload("first", target.getPath(), 7, digest);
            expectIOException(() -> manager.initializeUpload(upload, false));
            // Cleanup from a stale reference must not release the replacement's lease.
            expectIOException(() -> manager.reserveUpload("third", target.getPath(), 7, digest));
            assertFalse(replacement.part.exists());
        } finally {
            manager.closeAll();
            delete(dir);
        }
    }

    @Test public void slowCommitKeepsLeaseWithoutBlockingAnotherUpload() throws Exception {
        File dir = tempDir();
        AndroidFileManager manager = new AndroidFileManager(null);
        ExecutorService workers = Executors.newCachedThreadPool();
        CountDownLatch committing = new CountDownLatch(1);
        CountDownLatch release = new CountDownLatch(1);
        try {
            AndroidFileManager.Upload first = readyUpload(manager, dir, "first", "payload", "payload");
            Future<?> commit = workers.submit(() -> {
                manager.finishUpload(first, (part, target) -> {
                    committing.countDown();
                    await(release);
                    replace(part, target);
                });
                return null;
            });
            assertTrue(committing.await(5, TimeUnit.SECONDS));
            Future<?> independent = workers.submit(() -> {
                expectIOException(() -> manager.reserveUpload("collision", first.target.getPath(), 7, first.sha256));
                AndroidFileManager.Upload second = readyUpload(manager, dir, "second", "", "other");
                manager.writeUpload(second, 0, "other".getBytes(StandardCharsets.UTF_8));
                manager.finishUpload(second, AndroidFileManagerTest::replace);
                AndroidFileManager.Upload cancelled = readyUpload(manager, dir, "cancelled", "", "x");
                manager.cancel("cancelled");
                expectIOException(() -> manager.writeUpload(cancelled, 0, new byte[] { 1 }));
                return null;
            });
            independent.get(5, TimeUnit.SECONDS);
            assertEquals("other", read(new File(dir, "second")));
            assertFalse(commit.isDone());
            release.countDown();
            commit.get(5, TimeUnit.SECONDS);
            assertEquals("payload", read(first.target));
            assertFalse(first.sidecar.exists());
            manager.reserveUpload("after", first.target.getPath(), 7, first.sha256);
        } finally {
            release.countDown();
            workers.shutdownNow();
            assertTrue(workers.awaitTermination(5, TimeUnit.SECONDS));
            manager.closeAll();
            delete(dir);
        }
    }

    @Test public void cancelPreservesResumeAndRejectsStaleWritesAndFinalize() throws Exception {
        File dir = tempDir();
        AndroidFileManager manager = new AndroidFileManager(null);
        try {
            AndroidFileManager.Upload first = readyUpload(manager, dir, "first", "ab", "abcd");
            manager.cancel("first");
            assertEquals("ab", read(first.part));
            assertTrue(first.sidecar.exists());
            AndroidFileManager.Upload resumed = manager.reserveUpload("first", first.target.getPath(), 4, first.sha256);
            assertEquals(sha256("ab"), manager.initializeUpload(resumed, false));
            assertEquals(2, resumed.offset);
            expectIOException(() -> manager.writeUpload(first, 2, "XX".getBytes(StandardCharsets.UTF_8)));
            expectIOException(() -> manager.finishUpload(first, AndroidFileManagerTest::replace));
            expectIOException(() -> manager.reserveUpload("third", first.target.getPath(), 4, first.sha256));
            manager.writeUpload(resumed, 2, "cd".getBytes(StandardCharsets.UTF_8));
            manager.finishUpload(resumed, AndroidFileManagerTest::replace);
            assertEquals("abcd", read(first.target));
        } finally {
            manager.closeAll();
            delete(dir);
        }
    }

    @Test public void failedCommitReleasesLeaseButKeepsResumablePartial() throws Exception {
        File dir = tempDir();
        AndroidFileManager manager = new AndroidFileManager(null);
        try {
            AndroidFileManager.Upload upload = readyUpload(manager, dir, "first", "payload", "payload");
            write(upload.target, "original");
            expectIOException(() -> manager.finishUpload(upload, (part, target) -> { throw new IOException("injected rename failure"); }));
            assertEquals("original", read(upload.target));
            assertEquals("payload", read(upload.part));
            assertTrue(upload.sidecar.exists());
            assertTrue(upload.out == null);
            AndroidFileManager.Upload resumed = manager.reserveUpload("second", upload.target.getPath(), 7, upload.sha256);
            manager.initializeUpload(resumed, false);
            manager.finishUpload(resumed, AndroidFileManagerTest::replace);
            assertEquals("payload", read(upload.target));
        } finally {
            manager.closeAll();
            delete(dir);
        }
    }

    @Test public void failedInitializationReleasesDestination() throws Exception {
        File dir = tempDir();
        AndroidFileManager manager = new AndroidFileManager(null);
        try {
            File parent = new File(dir, "not-a-directory");
            write(parent, "file");
            String path = new File(parent, "target").getPath();
            AndroidFileManager.Upload upload = manager.reserveUpload("first", path, 1, sha256("x"));
            expectIOException(() -> manager.initializeUpload(upload, false));
            assertTrue(upload.out == null);
            manager.reserveUpload("second", path, 1, sha256("x"));
        } finally {
            manager.closeAll();
            delete(dir);
        }
    }

    @Test public void disconnectDropsQueuedStartAndMakesOldHandlesInert() throws Exception {
        File dir = tempDir();
        AndroidFileManager manager = new AndroidFileManager(null);
        ExecutorService workers = Executors.newSingleThreadExecutor();
        CountDownLatch entered = new CountDownLatch(1);
        CountDownLatch release = new CountDownLatch(1);
        CountDownLatch exited = new CountDownLatch(1);
        java.util.concurrent.atomic.AtomicBoolean queuedRan = new java.util.concurrent.atomic.AtomicBoolean();
        try {
            AndroidFileManager.Upload upload = readyUpload(manager, dir, "first", "ab", "abcd");
            manager.dispatch("blocked", () -> {
                entered.countDown();
                try { await(release); } catch (IOException e) { throw new AssertionError(e); }
                finally { exited.countDown(); }
            });
            assertTrue(entered.await(5, TimeUnit.SECONDS));
            manager.dispatch("blocked", () -> queuedRan.set(true));
            workers.submit(manager::closeAll).get(5, TimeUnit.SECONDS);
            expectIOException(() -> manager.reserveUpload("new", new File(dir, "new").getPath(), 1, sha256("x")));
            expectIOException(() -> manager.writeUpload(upload, 2, new byte[] { 1 }));
            expectIOException(() -> manager.finishUpload(upload, AndroidFileManagerTest::replace));
            assertTrue(upload.out == null);
            release.countDown();
            assertTrue(exited.await(5, TimeUnit.SECONDS));
            assertFalse(queuedRan.get());
            assertEquals("ab", read(upload.part));
            AndroidFileManager reconnected = new AndroidFileManager(null);
            try {
                AndroidFileManager.Upload resumed = reconnected.reserveUpload("first", upload.target.getPath(), 4, upload.sha256);
                assertEquals(sha256("ab"), reconnected.initializeUpload(resumed, false));
                reconnected.writeUpload(resumed, 2, "cd".getBytes(StandardCharsets.UTF_8));
                reconnected.finishUpload(resumed, AndroidFileManagerTest::replace);
                assertEquals("abcd", read(upload.target));
            } finally { reconnected.closeAll(); }
        } finally {
            release.countDown();
            workers.shutdownNow();
            manager.closeAll();
            delete(dir);
        }
    }

    private static AndroidFileManager.Upload readyUpload(AndroidFileManager manager, File dir, String taskId, String partial, String complete) throws Exception {
        String digest = sha256(complete);
        File target = new File(dir, taskId);
        // Seed a valid sidecar so host JVM tests exercise real IO without Android Os.rename.
        write(new File(target.getPath() + ".rdevpart"), partial);
        write(new File(target.getPath() + ".rdevpart.meta"), digest + "\n" + complete.length() + "\n");
        AndroidFileManager.Upload upload = manager.reserveUpload(taskId, target.getPath(), complete.length(), digest);
        assertEquals(sha256(partial), manager.initializeUpload(upload, false));
        return upload;
    }

    private interface Operation { void run() throws Exception; }

    private static void expectIOException(Operation operation) throws Exception {
        try {
            operation.run();
            throw new AssertionError("operation unexpectedly succeeded");
        } catch (IOException expected) { }
    }

    private static void await(CountDownLatch latch) throws IOException {
        try {
            if (!latch.await(5, TimeUnit.SECONDS)) throw new IOException("latch timed out");
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
            throw new IOException(e);
        }
    }

    private static void replace(File part, File target) throws IOException {
        java.nio.file.Files.move(part.toPath(), target.toPath(), java.nio.file.StandardCopyOption.REPLACE_EXISTING);
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
