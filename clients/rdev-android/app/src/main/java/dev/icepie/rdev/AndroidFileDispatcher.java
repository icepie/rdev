package dev.icepie.rdev;

import java.util.ArrayDeque;
import java.util.HashMap;
import java.util.Map;
import java.util.concurrent.LinkedBlockingQueue;
import java.util.concurrent.ThreadPoolExecutor;
import java.util.concurrent.TimeUnit;

// Connection-scoped FIFO lanes. Admission applies transport backpressure when
// the bounded worker/queue budget fills; disk IO never holds this monitor.
final class AndroidFileDispatcher {
    static final int WORKER_COUNT = 4;
    static final int MAX_PENDING_OPERATIONS = 128;
    static final int MAX_PENDING_BYTES = 1024 * 1024;
    private final Map<String, ArrayDeque<Pending>> pending = new HashMap<>();
    private final ThreadPoolExecutor workers = new ThreadPoolExecutor(
        WORKER_COUNT, WORKER_COUNT, 0, TimeUnit.MILLISECONDS,
        new LinkedBlockingQueue<>(MAX_PENDING_OPERATIONS), operation -> {
            Thread thread = new Thread(operation, "rdev-file-upload");
            thread.setDaemon(true);
            return thread;
        });
    private int pendingOperations;
    private int pendingBytes;
    private boolean closed;

    void execute(String taskId, Runnable operation) {
        execute(taskId, 0, operation);
    }

    synchronized void execute(String taskId, int bytes, Runnable operation) {
        if (bytes < 0 || bytes > MAX_PENDING_BYTES) throw new IllegalArgumentException("invalid queued payload size");
        while (!closed && (pendingOperations >= MAX_PENDING_OPERATIONS || bytes > MAX_PENDING_BYTES - pendingBytes)) {
            try { wait(); }
            catch (InterruptedException e) {
                Thread.currentThread().interrupt();
                return;
            }
        }
        if (closed) return;
        Pending item = new Pending(bytes, operation);
        pendingOperations++;
        pendingBytes += bytes;
        ArrayDeque<Pending> lane = pending.get(taskId);
        if (lane == null) {
            lane = new ArrayDeque<>();
            pending.put(taskId, lane);
            lane.addLast(item);
            runNext(taskId, lane);
        } else {
            lane.addLast(item);
        }
    }

    // Called only while holding this monitor; the operation itself runs without it.
    private void runNext(String taskId, ArrayDeque<Pending> lane) {
        Pending item = lane.peekFirst();
        workers.execute(() -> {
            synchronized (AndroidFileDispatcher.this) {
                if (closed) return;
            }
            try {
                item.operation.run();
            } finally {
                synchronized (AndroidFileDispatcher.this) {
                    if (!closed) {
                        pendingOperations--;
                        pendingBytes -= item.bytes;
                        lane.removeFirst();
                        if (lane.isEmpty()) pending.remove(taskId);
                        else runNext(taskId, lane);
                        AndroidFileDispatcher.this.notifyAll();
                    }
                }
            }
        });
    }

    synchronized void close() {
        closed = true;
        for (ArrayDeque<Pending> lane : pending.values()) lane.clear();
        pending.clear();
        pendingOperations = 0;
        pendingBytes = 0;
        workers.getQueue().clear();
        notifyAll();
        // Active IO owns its upload lock until cleanup; do not interrupt a commit.
        workers.shutdown();
    }

    private static final class Pending {
        final int bytes;
        final Runnable operation;
        Pending(int bytes, Runnable operation) {
            this.bytes = bytes;
            this.operation = operation;
        }
    }
}
