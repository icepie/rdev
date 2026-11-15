package dev.icepie.rdev;

import android.os.Environment;
import android.system.Os;
import android.system.OsConstants;
import android.system.ErrnoException;
import org.autojs.autojs.model.explorer.PrivilegedFileNative;
import android.util.Log;

import org.json.JSONArray;
import org.json.JSONObject;

import java.io.File;
import java.io.FileInputStream;
import java.io.FileOutputStream;
import java.io.IOException;
import java.io.BufferedReader;
import java.io.InputStreamReader;
import java.nio.channels.FileChannel;
import java.security.MessageDigest;
import java.security.NoSuchAlgorithmException;
import java.util.ArrayList;
import java.util.Arrays;
import java.text.SimpleDateFormat;
import java.util.Date;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Locale;
import java.util.Map;
import java.util.TimeZone;
import java.util.concurrent.ConcurrentHashMap;
import java.util.regex.Pattern;
final class AndroidFileManager {
    private static final String TAG = "RDevFiles";
    private static final int LIST_LIMIT = 200;
    private static final int CHUNK_SIZE = 16 * 1024;
    private static final Pattern SHA256 = Pattern.compile("[0-9a-f]{64}");
    private static final long FINGERPRINT_TTL_MS = 5 * 60 * 1000L;
    private static final int FINGERPRINT_LIMIT = 32;
    private final RDevAgentService service;
    private final Map<String, DirectorySnapshot> directoryCache = new LinkedHashMap<>();
    private final Map<String, Upload> uploads = new ConcurrentHashMap<>();
    private final Map<String, Download> downloads = new ConcurrentHashMap<>();
    private final LinkedHashMap<String, Fingerprint> fingerprints = new LinkedHashMap<>();

    AndroidFileManager(RDevAgentService service) {
        this.service = service;
    }

    void handleList(JSONObject msg) {
        String requestId = msg.optString("requestId", "");
        String path = canonicalPath(defaultPath(msg.optString("path", "")));
        File dir = new File(path);
        Log.i(TAG, "list start request=" + requestId + " path=" + path);
        try {
            int offset = Math.max(0, msg.optInt("offset", 0));
            File[] files = cachedDirectory(dir, offset);
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

    synchronized void handleUploadStart(JSONObject msg) {
        String taskId = msg.optString("taskId", "");
        if (taskId.isEmpty()) return;
        String path = msg.optString("path", "");
        if (path.isEmpty()) path = safeJoin(msg.optString("parentPath", ""), msg.optString("name", ""));
        String sha256 = msg.optString("sha256", "");
        long expected = msg.optLong("size", -1);
        try {
            if (!validSha256(sha256) || expected < 0) throw new IOException("invalid upload integrity metadata");
            File target = mutationTarget(path);
            File parent = target.getParentFile();
            if (parent != null && !parent.exists() && !parent.mkdirs()) throw new IOException("mkdir failed: " + parent);
            if (uploads.containsKey(taskId)) throw new IOException("upload already active for this task");
            for (Upload active : uploads.values()) if (active.target.equals(target)) throw new IOException("upload already active for this destination");
            File part = new File(target.getPath() + ".rdevpart");
            File sidecar = sidecar(part);
            long offset = prepareUploadPartial(part, sidecar, sha256, expected, msg.optBoolean("restart", false));
            if (offset > expected) offset = prepareUploadPartial(part, sidecar, sha256, expected, true);
            String prefixSha256 = hashPrefix(part, offset);
            FileOutputStream out = new FileOutputStream(part, true);
            Upload upload = new Upload(target, part, sidecar, out, expected, sha256, offset);
            uploads.put(taskId, upload);
            service.sendText(new JSONObject().put("type", "file_upload_ready").put("taskId", taskId)
                .put("path", target.getAbsolutePath()).put("offset", offset).put("size", expected)
                .put("sha256", sha256).put("prefixSha256", prefixSha256));
        } catch (Exception e) {
            sendError(taskId, path, e.getMessage());
        }
    }

    synchronized void handleUploadChunk(String taskId, long offset, byte[] data) {
        Upload upload = uploads.get(taskId);
        if (upload == null) { sendError(taskId, "", "upload not found"); return; }
        try {
            if (offset != upload.offset) throw new IOException("unexpected offset " + offset + ", want " + upload.offset);
            if (!validUploadChunk(offset, data == null ? 0 : data.length, upload.size)) throw new IOException("invalid upload chunk size");
            upload.out.write(data);
            upload.offset += data.length;
            service.sendBinaryOffset(RDevProtocol.BIN_FILE_UPLOAD_ACK, taskId, upload.offset, new byte[0]);
        } catch (Exception e) {
            uploads.remove(taskId);
            upload.close();
            sendError(taskId, upload.target.getAbsolutePath(), e.getMessage());
        }
    }

    synchronized void handleUploadEnd(JSONObject msg) {
        String taskId = msg.optString("taskId", "");
        Upload upload = uploads.remove(taskId);
        if (upload == null) { sendError(taskId, msg.optString("path", ""), "upload not found"); return; }
        try {
            upload.out.getFD().sync();
            upload.close();
            if (upload.offset != upload.size || upload.part.length() != upload.size) throw new IOException("upload size mismatch");
            publishUploadPart(upload.part, upload.target, upload.sidecar, upload.size, upload.sha256, AndroidFileManager::atomicReplace);
            if (upload.sidecar.exists()) upload.sidecar.delete();
            synchronized (directoryCache) { directoryCache.clear(); }
            synchronized (fingerprints) { fingerprints.remove(upload.target.getCanonicalPath()); }
            service.sendText(new JSONObject().put("type", "file_transfer_end").put("taskId", taskId)
                .put("path", upload.target.getAbsolutePath()).put("size", upload.offset).put("sha256", upload.sha256).put("success", true));
        } catch (Exception e) {
            sendError(taskId, upload.target.getAbsolutePath(), e.getMessage());
        }
    }

    void handleDownloadStart(JSONObject msg) {
        String taskId = msg.optString("taskId", "");
        String path = canonicalPath(msg.optString("path", ""));
        if (taskId.length() == 0 || path.length() == 0) return;
        Download download = new Download();
        Download old = downloads.put(taskId, download);
        if (old != null) old.cancelled = true;
        new Thread(() -> download(taskId, path, msg.optLong("offset", 0), msg.optLong("length", 0), msg.optString("sha256", ""), download), "rdev-file-download").start();
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

    void handleCreate(JSONObject msg) {
        String requestId = msg.optString("requestId", "");
        String path = msg.optString("path", "");
        try {
            File target = mutationTarget(path);
            if (!target.createNewFile()) throw new IOException("destination already exists");
            sendOpResult("file_create_result", requestId, target.getPath(), true, "");
        } catch (Exception e) {
            sendOpResult("file_create_result", requestId, path, false, e.getMessage());
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
        String path = msg.optString("path", "");
        try {
            File file = mutationTarget(path);
            deleteRecursive(file, msg.optBoolean("recursive", msg.optBoolean("success", false)));
            sendOpResult("file_delete_result", requestId, path, true, "");
        } catch (Exception e) {
            sendOpResult("file_delete_result", requestId, path, false, e.getMessage());
        }
    }

    void handleRename(JSONObject msg) {
        String requestId = msg.optString("requestId", "");
        String from = msg.optString("path", msg.optString("from", msg.optString("data", "")));
        String to = msg.optString("to", msg.optString("filePath", ""));
        try {
            File source = mutationTarget(from);
            File target = mutationTarget(to);
            if (existsWithoutFollowing(target)) throw new IOException("destination already exists");
            File parent = target.getParentFile();
            if (parent != null && !parent.exists() && !parent.mkdirs()) throw new IOException("mkdir failed: " + parent);
            int errno = PrivilegedFileNative.renameNoReplace(source.getPath(), target.getPath());
            if (errno != 0) throw new ErrnoException("renameat2", errno);
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

    synchronized void cancel(String taskId) {
        Upload upload = uploads.remove(taskId);
        if (upload != null) upload.close();
        Download download = downloads.remove(taskId);
        if (download != null) download.cancelled = true;
    }

    synchronized void closeAll() {
        List<String> ids = new ArrayList<>(uploads.keySet());
        ids.addAll(downloads.keySet());
        for (String id : ids) cancel(id);
    }

    private void download(String taskId, String path, long offset, long length, String requestedSha256, Download download) {
        File file = new File(path);
        try (FileInputStream in = new FileInputStream(file)) {
            if (!file.isFile()) throw new IOException("cannot download directory");
            Fingerprint fingerprint = fingerprint(file);
            long size = fingerprint.size;
            if (offset < 0 || offset > size) throw new IOException("invalid download offset");
            if (!requestedSha256.isEmpty() && (!validSha256(requestedSha256) || !fingerprint.sha256.equals(requestedSha256)))
                throw new IOException("download source changed; restart from offset zero");
            if (offset > 0 && !validSha256(requestedSha256)) throw new IOException("resumed download requires source SHA-256");
            long skipped = in.skip(offset);
            if (skipped != offset) throw new IOException("seek failed");
            service.sendText(new JSONObject().put("type", "file_download_start").put("taskId", taskId)
                .put("path", file.getAbsolutePath()).put("name", file.getName()).put("size", size)
                .put("offset", offset).put("sha256", fingerprint.sha256).put("modTime", rfc3339(file.lastModified())));
            if (length < 0) throw new IOException("invalid download length");
            long end = length > 0 ? offset + Math.min(length, size - offset) : size;
            byte[] buffer = new byte[CHUNK_SIZE];
            long cur = offset;
            while (!download.cancelled && cur < end) {
                int n = in.read(buffer, 0, (int) Math.min(buffer.length, end - cur));
                if (n < 0) break;
                if (n > 0) {
                    if (cur > end - n) throw new IOException("download source grew during transfer");
                    if (!service.sendBinaryOffset(RDevProtocol.BIN_FILE_DOWNLOAD_CHUNK, taskId, cur, buffer, n)) { download.cancelled = true; break; }
                    cur += n;
                    try { Thread.sleep(2); } catch (InterruptedException e) { Thread.currentThread().interrupt(); download.cancelled = true; break; }
                }
            }
            if (!download.cancelled && (cur != end || !fingerprint.matches(file))) throw new IOException("download source changed during transfer");
            if (!download.cancelled && service.sendBinaryOffset(RDevProtocol.BIN_FILE_TRANSFER_END, taskId, cur, new byte[0]))
                service.sendText(new JSONObject().put("type", "file_transfer_end").put("taskId", taskId)
                    .put("path", file.getAbsolutePath()).put("size", size).put("offset", cur)
                    .put("sha256", fingerprint.sha256).put("success", true));
        } catch (Exception e) {
            sendError(taskId, path, e.getMessage());
        } finally {
            if (downloads.get(taskId) == download) downloads.remove(taskId);
        }
    }
    private File sidecar(File part) { return new File(part.getPath() + ".meta"); }

    private boolean readSidecar(File sidecar, String sha256, long size) {
        try (BufferedReader reader = new BufferedReader(new InputStreamReader(new FileInputStream(sidecar), "UTF-8"))) {
            String digest = reader.readLine();
            String declaredSize = reader.readLine();
            String extra = reader.readLine();
            return sidecarMatches(digest, declaredSize, extra, sha256, size);
        } catch (Exception e) { return false; }
    }

    static boolean sidecarMatches(String digest, String declaredSize, String extra, String sha256, long size) {
        return sha256.equals(digest) && Long.toString(size).equals(declaredSize) && extra == null;
    }
    long prepareUploadPartial(File part, File sidecar, String sha256, long size, boolean restart) throws IOException {
        boolean resumable = !restart && part.isFile() && readSidecar(sidecar, sha256, size);
        if (!resumable) {
            truncate(part);
            writeSidecar(sidecar, sha256, size);
        }
        return part.length();
    }

    void publishUploadPart(File part, File target, File sidecar, long size, String expectedSha256, UploadReplacer replacer) throws Exception {
        if (part.length() != size || !expectedSha256.equals(sha256(part))) {
            if (sidecar.exists()) sidecar.delete();
            throw new IOException("upload SHA-256 mismatch");
        }
        replacer.replace(part, target);
        if (sidecar.exists()) sidecar.delete();
    }

    static void atomicReplace(File part, File target) throws IOException {
        try { Os.rename(part.getPath(), target.getPath()); }
        catch (ErrnoException e) { throw new IOException("atomic replacement failed", e); }
    }

    interface UploadReplacer { void replace(File part, File target) throws IOException; }


    private void writeSidecar(File sidecar, String sha256, long size) throws IOException {
        File temp = new File(sidecar.getPath() + ".tmp");
        try (FileOutputStream out = new FileOutputStream(temp, false)) {
            out.write((sha256 + "\n" + size + "\n").getBytes("UTF-8"));
            out.getFD().sync();
        }
        try { Os.rename(temp.getPath(), sidecar.getPath()); }
        catch (ErrnoException e) { temp.delete(); throw new IOException("sidecar replace failed", e); }
    }

    private void truncate(File file) throws IOException {
        try (FileOutputStream out = new FileOutputStream(file, false)) { out.getFD().sync(); }
    }

    private String hashPrefix(File file, long length) throws Exception {
        MessageDigest digest = newDigest();
        try (FileInputStream in = new FileInputStream(file)) {
            byte[] buffer = new byte[CHUNK_SIZE];
            long remaining = length;
            while (remaining > 0) {
                int n = in.read(buffer, 0, (int) Math.min(buffer.length, remaining));
                if (n < 0) throw new IOException("partial upload truncated");
                digest.update(buffer, 0, n);
                remaining -= n;
            }
        }
        return hex(digest.digest());
    }

    private String sha256(File file) throws Exception {
        MessageDigest digest = newDigest();
        try (FileInputStream in = new FileInputStream(file)) {
            byte[] buffer = new byte[CHUNK_SIZE];
            int n;
            while ((n = in.read(buffer)) >= 0) if (n > 0) digest.update(buffer, 0, n);
        }
        return hex(digest.digest());
    }

    private MessageDigest newDigest() throws IOException {
        try { return MessageDigest.getInstance("SHA-256"); }
        catch (NoSuchAlgorithmException e) { throw new IOException("SHA-256 unavailable", e); }
    }

    private String hex(byte[] bytes) {
        StringBuilder out = new StringBuilder(bytes.length * 2);
        for (byte value : bytes) out.append(String.format(Locale.US, "%02x", value & 0xff));
        return out.toString();
    }

    static boolean validSha256(String value) { return value != null && SHA256.matcher(value).matches(); }

    static boolean validUploadChunk(long offset, int length, long expected) {
        return offset >= 0 && length > 0 && length <= CHUNK_SIZE && offset <= expected && length <= expected - offset;
    }

    private Fingerprint fingerprint(File file) throws Exception {
        String path = file.getCanonicalPath();
        long now = android.os.SystemClock.elapsedRealtime();
        Fingerprint current = new Fingerprint(file);
        synchronized (fingerprints) {
            Fingerprint cached = fingerprints.get(path);
            if (cached != null && now - cached.created <= FINGERPRINT_TTL_MS && cached.sameSource(current)) return cached;
        }
        current.sha256 = sha256(file);
        if (!current.matches(file)) throw new IOException("download source changed while hashing");
        current.created = now;
        synchronized (fingerprints) {
            fingerprints.remove(path);
            while (fingerprints.size() >= FINGERPRINT_LIMIT) fingerprints.remove(fingerprints.keySet().iterator().next());
            fingerprints.put(path, current);
        }
        return current;
    }


    private static final class DirectorySnapshot {
        final File[] files;
        final long modified, at, bytes;
        DirectorySnapshot(File[] files, long modified, long bytes) {
            this.files = files; this.modified = modified; this.bytes = bytes;
            this.at = android.os.SystemClock.elapsedRealtime();
        }
    }

    private File[] cachedDirectory(File dir, int offset) throws IOException {
        String path = dir.getCanonicalPath();
        long now = android.os.SystemClock.elapsedRealtime();
        long modified = dir.lastModified();
        synchronized (directoryCache) {
            directoryCache.entrySet().removeIf(entry -> now - entry.getValue().at >= 10000);
            DirectorySnapshot snapshot = directoryCache.get(path);
            if (offset > 0 && snapshot != null && snapshot.modified == modified) return snapshot.files;
        }
        File[] files = dir.listFiles();
        if (files == null) throw new IOException("cannot list directory");
        Map<File, Boolean> directories = new java.util.HashMap<>();
        long bytes = 0;
        for (File file : files) { directories.put(file, file.isDirectory()); bytes += file.getPath().length() * 2L + 96; }
        Arrays.sort(files, (a, b) -> {
            if (!directories.get(a).equals(directories.get(b))) return directories.get(a) ? -1 : 1;
            int order = a.getName().compareToIgnoreCase(b.getName());
            return order != 0 ? order : a.getName().compareTo(b.getName());
        });
        synchronized (directoryCache) {
            directoryCache.remove(path);
            if (bytes <= 32 * 1024 * 1024 && dir.lastModified() == modified) {
                long total = bytes;
                for (DirectorySnapshot snapshot : directoryCache.values()) total += snapshot.bytes;
                while (!directoryCache.isEmpty() && (directoryCache.size() >= 8 || total > 32 * 1024 * 1024)) {
                    String oldest = directoryCache.keySet().iterator().next();
                    total -= directoryCache.remove(oldest).bytes;
                }
                directoryCache.put(path, new DirectorySnapshot(files, modified, bytes));
            }
        }
        return files;
    }

    private JSONObject entry(File file) throws Exception {
        return new JSONObject()
            .put("name", file.getName())
            .put("path", file.getAbsolutePath())
            .put("isDir", file.isDirectory())
            .put("size", file.isDirectory() ? 0 : file.length())
            .put("modTime", rfc3339(file.lastModified()));
    }

    // Resolve parent directories, but never follow the final symbolic link.
    private File mutationTarget(String path) throws IOException {
        if (path == null || path.trim().isEmpty()) throw new IOException("missing path");
        File absolute = new File(path).getAbsoluteFile();
        if (absolute.getParentFile() == null || absolute.getName().equals(".") || absolute.getName().equals(".."))
            throw new IOException("cannot modify filesystem root or relative marker");
        return new File(absolute.getParentFile().getCanonicalFile(), absolute.getName());
    }

    private boolean existsWithoutFollowing(File file) throws IOException {
        try { Os.lstat(file.getPath()); return true; }
        catch (ErrnoException e) {
            if (e.errno == OsConstants.ENOENT) return false;
            throw new IOException(e);
        }
    }

    private void deleteRecursive(File file, boolean recursive) throws IOException {
        if (!existsWithoutFollowing(file)) return;
        boolean directory;
        try { directory = OsConstants.S_ISDIR(Os.lstat(file.getPath()).st_mode); }
        catch (ErrnoException e) { throw new IOException(e); }
        if (directory) {
            File[] children = file.listFiles();
            if (children == null) throw new IOException("cannot list directory");
            if (children.length > 0 && !recursive) throw new IOException("directory not empty");
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
        if (success) { synchronized (directoryCache) { directoryCache.clear(); } }
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

    private static final class Fingerprint {
        final String path;
        final long size;
        final long modified;
        final long changed;
        final long device;
        final long inode;
        String sha256;
        long created;
        Fingerprint(File file) throws IOException {
            this.path = file.getCanonicalPath();
            try {
                android.system.StructStat stat = Os.stat(file.getPath());
                this.size = stat.st_size;
                this.modified = stat.st_mtime;
                this.changed = stat.st_ctime;
                this.device = stat.st_dev;
                this.inode = stat.st_ino;
            } catch (ErrnoException e) { throw new IOException("cannot stat download source", e); }
        }
        boolean sameSource(Fingerprint other) {
            return path.equals(other.path) && size == other.size && modified == other.modified && changed == other.changed && device == other.device && inode == other.inode;
        }
        boolean matches(File file) throws IOException { return sameSource(new Fingerprint(file)); }
    }

    private static final class Upload {
        final File target;
        final File part;
        final File sidecar;
        final FileOutputStream out;
        final long size;
        final String sha256;
        long offset;
        Upload(File target, File part, File sidecar, FileOutputStream out, long size, String sha256, long offset) {
            this.target = target;
            this.part = part;
            this.sidecar = sidecar;
            this.out = out;
            this.size = size;
            this.sha256 = sha256;
            this.offset = offset;
        }
        void close() {
            try { out.close(); } catch (IOException ignored) {}
        }
    }

    private static final class Download { volatile boolean cancelled; }
}
