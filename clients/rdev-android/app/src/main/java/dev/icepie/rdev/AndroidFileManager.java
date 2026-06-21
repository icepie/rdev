package dev.icepie.rdev;

import android.os.Environment;
import android.util.Log;

import org.json.JSONArray;
import org.json.JSONObject;

import java.io.File;
import java.io.FileInputStream;
import java.io.FileOutputStream;
import java.io.IOException;
import java.nio.channels.FileChannel;
import java.text.SimpleDateFormat;
import java.util.ArrayList;
import java.util.Arrays;
import java.util.Date;
import java.util.List;
import java.util.Locale;
import java.util.Map;
import java.util.TimeZone;
import java.util.concurrent.ConcurrentHashMap;

final class AndroidFileManager {
    private static final String TAG = "RDevFiles";
    private static final int LIST_LIMIT = 200;
    private static final int CHUNK_SIZE = 16 * 1024;
    private final RDevAgentService service;
    private final Map<String, Upload> uploads = new ConcurrentHashMap<>();
    private final Map<String, Download> downloads = new ConcurrentHashMap<>();

    AndroidFileManager(RDevAgentService service) {
        this.service = service;
    }

    void handleList(JSONObject msg) {
        String requestId = msg.optString("requestId", "");
        String path = canonicalPath(defaultPath(msg.optString("path", "")));
        File dir = new File(path);
        Log.i(TAG, "list start request=" + requestId + " path=" + path);
        try {
            File[] files = dir.listFiles();
            if (files == null) throw new IOException("cannot list directory");
            Arrays.sort(files, (a, b) -> {
                if (a.isDirectory() != b.isDirectory()) return a.isDirectory() ? -1 : 1;
                return a.getName().compareToIgnoreCase(b.getName());
            });
            int offset = Math.max(0, msg.optInt("offset", 0));
            int limit = msg.has("limit") ? msg.optInt("limit", LIST_LIMIT) : (msg.optLong("size", 0) > 0 ? (int) msg.optLong("size", LIST_LIMIT) : LIST_LIMIT);
            limit = Math.max(1, Math.min(LIST_LIMIT, limit));
            boolean truncated = files.length > offset + limit;
            int count = Math.max(0, Math.min(files.length - offset, limit));
            JSONArray entries = new JSONArray();
            for (int i = 0; i < count; i++) entries.put(entry(files[offset + i]));
            JSONObject out = new JSONObject()
                .put("type", "file_list_result")
                .put("requestId", requestId)
                .put("path", dir.getCanonicalPath())
                .put("parentPath", parentPath(dir))
                .put("homePath", homePath())
                .put("offset", offset)
                .put("limit", limit)
                .put("total", files.length)
                .put("fileMode", files.length)
                .put("truncated", truncated)
                .put("entries", entries);
            service.sendText(out);
            Log.i(TAG, "list end request=" + requestId + " path=" + dir.getAbsolutePath() + " offset=" + offset + " count=" + count + " total=" + files.length + " truncated=" + truncated);
        } catch (Exception e) {
            Log.w(TAG, "list failed request=" + requestId + " path=" + path, e);
            try {
                service.sendText(new JSONObject()
                    .put("type", "file_list_result")
                    .put("requestId", requestId)
                    .put("path", path)
                    .put("error", e.getMessage()));
            } catch (Exception ignored) {}
        }
    }

    void handleUploadStart(JSONObject msg) {
        String taskId = msg.optString("taskId", "");
        Log.i(TAG, "upload start task=" + taskId + " path=" + msg.optString("path", "") + " parent=" + msg.optString("parentPath", "") + " name=" + msg.optString("name", ""));
        if (taskId.length() == 0) return;
        String path = msg.optString("path", "");
        if (path.length() == 0) path = safeJoin(msg.optString("parentPath", ""), msg.optString("name", ""));
        path = canonicalPath(path);
        if (path.length() == 0) {
            sendError(taskId, "", "missing target path");
            return;
        }
        try {
            File target = new File(path);
            File parent = target.getParentFile();
            if (parent != null && !parent.exists() && !parent.mkdirs()) throw new IOException("mkdir failed: " + parent);
            File part = new File(path + ".rdevpart");
            FileOutputStream out = new FileOutputStream(part, true);
            long offset = part.length();
            Upload old = uploads.put(taskId, new Upload(target, part, out, msg.optLong("size", -1), offset));
            if (old != null) old.close();
            service.sendText(new JSONObject()
                .put("type", "file_upload_ready")
                .put("taskId", taskId)
                .put("path", target.getAbsolutePath())
                .put("offset", offset)
                .put("size", msg.optLong("size", -1)));
        } catch (Exception e) {
            sendError(taskId, path, e.getMessage());
        }
    }

    void handleUploadChunk(String taskId, long offset, byte[] data) {
        Upload upload = uploads.get(taskId);
        if (upload == null) {
            sendError(taskId, "", "upload not found");
            return;
        }
        try {
            if (offset != upload.offset) throw new IOException("unexpected offset " + offset + ", want " + upload.offset);
            upload.out.write(data);
            upload.offset += data.length;
            service.sendBinaryOffset(RDevProtocol.BIN_FILE_UPLOAD_ACK, taskId, upload.offset, new byte[0]);
        } catch (Exception e) {
            sendError(taskId, upload.target.getAbsolutePath(), e.getMessage());
        }
    }

    void handleUploadEnd(JSONObject msg) {
        String taskId = msg.optString("taskId", "");
        Upload upload = uploads.remove(taskId);
        if (upload == null) {
            sendError(taskId, msg.optString("path", ""), "upload not found");
            return;
        }
        try {
            upload.out.getFD().sync();
            upload.close();
            if (upload.size >= 0 && upload.offset != upload.size) throw new IOException("size mismatch: wrote " + upload.offset + " of " + upload.size);
            if (upload.target.exists() && !upload.target.delete()) throw new IOException("replace failed: " + upload.target);
            if (!upload.part.renameTo(upload.target)) throw new IOException("rename failed: " + upload.part);
            service.sendText(new JSONObject()
                .put("type", "file_transfer_end")
                .put("taskId", taskId)
                .put("path", upload.target.getAbsolutePath())
                .put("size", upload.offset)
                .put("success", true));
        } catch (Exception e) {
            sendError(taskId, upload.target.getAbsolutePath(), e.getMessage());
        }
    }

    void handleDownloadStart(JSONObject msg) {
        String taskId = msg.optString("taskId", "");
        String path = canonicalPath(msg.optString("path", ""));
        Log.i(TAG, "download request task=" + taskId + " path=" + path);
        if (taskId.length() == 0 || path.length() == 0) return;
        Download download = new Download();
        Download old = downloads.put(taskId, download);
        if (old != null) old.cancelled = true;
        new Thread(() -> download(taskId, path, msg.optLong("offset", 0), download), "rdev-file-download").start();
    }

    void handleStat(JSONObject msg) {
        String requestId = msg.optString("requestId", "");
        String path = canonicalPath(defaultPath(msg.optString("path", "")));
        try {
            service.sendText(new JSONObject()
                .put("type", "file_stat_result")
                .put("requestId", requestId)
                .put("path", path)
                .put("success", true)
                .put("entries", new JSONArray().put(entry(new File(path)))));
        } catch (Exception e) {
            sendOpResult("file_stat_result", requestId, path, false, e.getMessage());
        }
    }

    void handleMkdir(JSONObject msg) {
        String requestId = msg.optString("requestId", "");
        String path = msg.optString("path", "");
        if (path.length() == 0) path = safeJoin(msg.optString("parentPath", ""), msg.optString("name", ""));
        path = canonicalPath(path);
        try {
            File dir = new File(path);
            if (!dir.exists() && !dir.mkdirs()) throw new IOException("mkdir failed");
            sendOpResult("file_mkdir_result", requestId, dir.getAbsolutePath(), true, "");
        } catch (Exception e) {
            sendOpResult("file_mkdir_result", requestId, path, false, e.getMessage());
        }
    }

    void handleDelete(JSONObject msg) {
        String requestId = msg.optString("requestId", "");
        String path = canonicalPath(msg.optString("path", ""));
        try {
            File file = new File(path);
            deleteRecursive(file, msg.optBoolean("recursive", msg.optBoolean("success", true)));
            sendOpResult("file_delete_result", requestId, path, true, "");
        } catch (Exception e) {
            sendOpResult("file_delete_result", requestId, path, false, e.getMessage());
        }
    }

    void handleRename(JSONObject msg) {
        String requestId = msg.optString("requestId", "");
        String from = canonicalPath(msg.optString("path", msg.optString("from", msg.optString("data", ""))));
        String to = canonicalPath(msg.optString("to", msg.optString("filePath", "")));
        if (to.length() == 0) to = canonicalPath(safeJoin(msg.optString("parentPath", ""), msg.optString("name", "")));
        try {
            File source = new File(from);
            File target = new File(to);
            File parent = target.getParentFile();
            if (parent != null && !parent.exists() && !parent.mkdirs()) throw new IOException("mkdir failed: " + parent);
            if (!source.renameTo(target)) throw new IOException("rename failed");
            sendOpResult("file_rename_result", requestId, target.getAbsolutePath(), true, "");
        } catch (Exception e) {
            sendOpResult("file_rename_result", requestId, from, false, e.getMessage());
        }
    }

    void handleCopy(JSONObject msg) {
        String requestId = msg.optString("requestId", "");
        String from = canonicalPath(msg.optString("path", msg.optString("from", msg.optString("data", ""))));
        String to = canonicalPath(msg.optString("to", msg.optString("filePath", "")));
        try {
            File source = new File(from);
            File target = new File(to);
            if (source.isDirectory()) throw new IOException("copy directory is not supported yet");
            File parent = target.getParentFile();
            if (parent != null && !parent.exists() && !parent.mkdirs()) throw new IOException("mkdir failed: " + parent);
            copyFile(source, target);
            sendOpResult("file_copy_result", requestId, target.getAbsolutePath(), true, "");
        } catch (Exception e) {
            sendOpResult("file_copy_result", requestId, from, false, e.getMessage());
        }
    }

    void cancel(String taskId) {
        Upload upload = uploads.remove(taskId);
        if (upload != null) upload.close();
        Download download = downloads.remove(taskId);
        if (download != null) download.cancelled = true;
    }

    void closeAll() {
        List<String> ids = new ArrayList<>(uploads.keySet());
        ids.addAll(downloads.keySet());
        for (String id : ids) cancel(id);
    }

    private void download(String taskId, String path, long offset, Download download) {
        File file = new File(path);
        try (FileInputStream in = new FileInputStream(file)) {
            if (!file.isFile()) throw new IOException("cannot download directory");
            Log.i(TAG, "download start task=" + taskId + " path=" + file.getAbsolutePath());
            long size = file.length();
            if (offset < 0 || offset > size) offset = 0;
            long skipped = in.skip(offset);
            if (skipped != offset) throw new IOException("seek failed");
            service.sendText(new JSONObject()
                .put("type", "file_download_start")
                .put("taskId", taskId)
                .put("path", file.getAbsolutePath())
                .put("name", file.getName())
                .put("size", size)
                .put("offset", offset)
                .put("modTime", rfc3339(file.lastModified())));
            byte[] buffer = new byte[CHUNK_SIZE];
            long cur = offset;
            while (!download.cancelled) {
                int n = in.read(buffer);
                if (n < 0) break;
                if (n > 0) {
                    if (!service.sendBinaryOffset(RDevProtocol.BIN_FILE_DOWNLOAD_CHUNK, taskId, cur, buffer, n)) {
                        download.cancelled = true;
                        break;
                    }
                    Log.d(TAG, "download chunk task=" + taskId + " offset=" + cur + " bytes=" + n);
                    cur += n;
                    try { Thread.sleep(2); } catch (InterruptedException e) { Thread.currentThread().interrupt(); download.cancelled = true; break; }
                }
            }
            if (!download.cancelled) {
                Log.i(TAG, "download end task=" + taskId + " offset=" + cur);
                if (service.sendBinaryOffset(RDevProtocol.BIN_FILE_TRANSFER_END, taskId, cur, new byte[0])) {
                    service.sendText(new JSONObject()
                        .put("type", "file_transfer_end")
                        .put("taskId", taskId)
                        .put("path", file.getAbsolutePath())
                        .put("size", size)
                        .put("offset", cur)
                        .put("success", true));
                }
            }
        } catch (Exception e) {
            sendError(taskId, path, e.getMessage());
        } finally {
            downloads.remove(taskId, download);
        }
    }

    private JSONObject entry(File file) throws Exception {
        return new JSONObject()
            .put("name", file.getName())
            .put("path", file.getAbsolutePath())
            .put("isDir", file.isDirectory())
            .put("size", file.isDirectory() ? 0 : file.length())
            .put("modTime", rfc3339(file.lastModified()));
    }

    private void deleteRecursive(File file, boolean recursive) throws IOException {
        if (!file.exists()) return;
        if (file.isDirectory()) {
            File[] children = file.listFiles();
            if (children != null && children.length > 0 && !recursive) throw new IOException("directory not empty");
            if (children != null) for (File child : children) deleteRecursive(child, true);
        }
        if (!file.delete()) throw new IOException("delete failed: " + file);
    }

    private void copyFile(File source, File target) throws IOException {
        try (FileInputStream in = new FileInputStream(source); FileOutputStream out = new FileOutputStream(target); FileChannel src = in.getChannel(); FileChannel dst = out.getChannel()) {
            long size = src.size();
            long pos = 0;
            while (pos < size) pos += src.transferTo(pos, Math.min(8 * 1024 * 1024, size - pos), dst);
            out.getFD().sync();
        }
    }

    private void sendOpResult(String type, String requestId, String path, boolean success, String error) {
        try {
            JSONObject out = new JSONObject()
                .put("type", type)
                .put("requestId", requestId)
                .put("path", path)
                .put("success", success);
            if (!success) out.put("error", error == null ? "unknown error" : error);
            service.sendText(out);
        } catch (Exception ignored) {}
    }

    private void sendError(String taskId, String path, String error) {
        try {
            service.sendText(new JSONObject()
                .put("type", "file_transfer_error")
                .put("taskId", taskId)
                .put("path", path)
                .put("error", error == null ? "unknown error" : error));
        } catch (Exception ignored) {}
    }

    private String defaultPath(String path) {
        return path == null || path.length() == 0 ? homePath() : path;
    }

    private String canonicalPath(String path) {
        if (path == null || path.length() == 0) return "";
        try { return new File(path).getCanonicalPath(); }
        catch (IOException e) { return new File(path).getAbsolutePath(); }
    }

    private String homePath() {
        try {
            File publicRoot = Environment.getExternalStorageDirectory();
            if (publicRoot != null && publicRoot.exists() && publicRoot.canRead()) return publicRoot.getAbsolutePath();
        } catch (Throwable ignored) {}
        File external = service.getExternalFilesDir(null);
        return external != null ? external.getAbsolutePath() : service.getFilesDir().getAbsolutePath();
    }

    private String parentPath(File file) throws IOException {
        File parent = file.getCanonicalFile().getParentFile();
        return parent != null ? parent.getAbsolutePath() : file.getCanonicalPath();
    }

    private String safeJoin(String parent, String name) {
        if (name == null || name.length() == 0) return defaultPath(parent);
        File file = new File(name);
        if (file.isAbsolute()) return file.getAbsolutePath();
        return new File(defaultPath(parent), file.getName()).getAbsolutePath();
    }

    private String rfc3339(long millis) {
        SimpleDateFormat fmt = new SimpleDateFormat("yyyy-MM-dd'T'HH:mm:ss'Z'", Locale.US);
        fmt.setTimeZone(TimeZone.getTimeZone("UTC"));
        return fmt.format(new Date(millis));
    }

    private static final class Upload {
        final File target;
        final File part;
        final FileOutputStream out;
        final long size;
        long offset;
        Upload(File target, File part, FileOutputStream out, long size, long offset) {
            this.target = target;
            this.part = part;
            this.out = out;
            this.size = size;
            this.offset = offset;
        }
        void close() {
            try { out.close(); } catch (IOException ignored) {}
        }
    }

    private static final class Download { volatile boolean cancelled; }
}
