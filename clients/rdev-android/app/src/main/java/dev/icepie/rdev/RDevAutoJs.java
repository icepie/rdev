package dev.icepie.rdev;

import android.os.Handler;
import android.os.Looper;
import android.util.Log;

import org.autojs.autojs.AutoJs;
import org.autojs.autojs.core.accessibility.AccessibilityService;
import org.autojs.autojs.core.console.ConsoleImpl;
import org.autojs.autojs.engine.JavaScriptEngine;
import org.autojs.autojs.engine.ScriptEngine;
import org.autojs.autojs.execution.ExecutionConfig;
import org.autojs.autojs.execution.ScriptExecution;
import org.autojs.autojs.execution.ScriptExecutionListener;
import org.autojs.autojs.runtime.exception.ScriptInterruptedException;
import org.autojs.autojs.script.StringScriptSource;
import org.json.JSONArray;
import org.json.JSONObject;

import java.util.ArrayList;
import java.util.List;

/** Thin adapter over AutoJs6's real Rhino ScriptEngineService. */
final class RDevAutoJs {
    private static final String TAG = "RDevAutoJs";
    static final String VERSION = "6.8.0";
    static final int MAX_SCRIPT_BYTES = 64 * 1024;
    static final int MAX_TIMEOUT_MS = 25_000;
    static final int MAX_RESPONSE_BYTES = 128 * 1024;
    private static final int MAX_LOG_BYTES = 48 * 1024;
    private static final int MAX_LOG_LINES = 256;
    private static final int MAX_TEXT_BYTES = 8 * 1024;
    private static final Handler MAIN = new Handler(Looper.getMainLooper());

    interface Completion { void onComplete(JSONObject result); }

    private RDevAutoJs() {}

    static JSONObject status() throws Exception {
        boolean available = AutoJs.getInstance() != null;
        return new JSONObject()
                .put("engine", "Auto.js")
                .put("version", VERSION)
                .put("accessibility", available && AccessibilityService.Companion.getInstance() != null);
    }

    static Run run(String script, int timeoutMs, Completion completion) {
        return new Run(script, timeoutMs, completion);
    }

    static final class Run {
        private final String script;
        private final int timeoutMs;
        private final Completion completion;
        private final List<String> logs = new ArrayList<>();
        private final Runnable timeout;
        private int logBytes;
        private boolean logsTruncated;
        private boolean done;
        private boolean started;
        private ScriptExecution execution;
        private ConsoleImpl console;
        private ConsoleImpl.LogListener logListener;

        private String pendingStop;
        private String pendingStopReason;
        Run(String script, int timeoutMs, Completion completion) {
            this.script = script;
            this.timeoutMs = timeoutMs;
            this.completion = completion;
            this.timeout = () -> stopAndFinish("timeout", "Script exceeded timeout of " + this.timeoutMs + "ms");
        }

        void start() {
            MAIN.post(() -> {
                synchronized (this) {
                    if (done) return;
                    started = true;
                }
                MAIN.postDelayed(timeout, timeoutMs);
                try {
                    AutoJs autoJs = AutoJs.getInstance();
                    if (autoJs == null) throw new IllegalStateException("AutoJs6 runtime is not initialized");
                    StringScriptSource source = new StringScriptSource("rdev-agent.js", script);
                    int mode = source.getExecutionMode();
                    if (!org.autojs.autojs.script.JavaScriptSource.ENGINE.equals(source.getEngineName()) ||
                            (mode & (StringScriptSource.EXECUTION_MODE_UI | StringScriptSource.EXECUTION_MODE_UI_THREAD)) != 0) {
                        throw new IllegalArgumentException("Only background Auto.js JavaScript scripts are supported");
                    }
                    if ((mode & StringScriptSource.EXECUTION_MODE_AUTO) != 0 && AccessibilityService.Companion.getInstance() == null) {
                        throw new IllegalStateException("Enable RDev AutoJs Scripts in Android accessibility settings before running automation scripts");
                    }
                    ExecutionConfig config = new ExecutionConfig();
                    config.setWorkingDirectory(autoJs.getApplication().getFilesDir().getAbsolutePath());
                    ScriptExecution startedExecution = autoJs.getScriptEngineService().execute(
                            source,
                            new ScriptExecutionListener() {
                                @Override public void onStart(ScriptExecution current) {
                                    onExecutionStart(current);
                                }
                                @Override public void onSuccess(ScriptExecution current, Object value) {
                                    finish("finished", value, null);
                                }
                                @Override public void onException(ScriptExecution current, Throwable error) {
                                    if (isInterrupted(error)) finish("canceled", null, errorText(error));
                                    else finish("error", null, errorText(error));
                                }
                            },
                            config
                    );
                    synchronized (this) {
                        execution = startedExecution;
                        String stop = pendingStop;
                        if (stop != null) forceStop(startedExecution);
                    }
                } catch (Throwable error) {
                    finish("error", null, errorText(error));
                }
            });
        }

        private void onExecutionStart(ScriptExecution current) {
            String stop;
            synchronized (this) {
                execution = current;
                stop = pendingStop;
                if (current.getEngine() instanceof JavaScriptEngine) {
                    console = ((JavaScriptEngine) current.getEngine()).getRuntime().console;
                    logListener = new ConsoleImpl.LogListener() {
                        @Override public void onNewLog(ConsoleImpl.LogEntry entry) { appendLog(entry); }
                        @Override public void onLogClear() { }
                    };
                    console.addLogListener(logListener);
                }
            }
            if (stop != null) {
                forceStop(current);
                throw new ScriptInterruptedException();
            }
        }

        private void appendLog(ConsoleImpl.LogEntry entry) {
            String line = entry.content.toString();
            int bytes = utf8Length(line);
            synchronized (this) {
                if (done) return;
                if (logs.size() >= MAX_LOG_LINES || logBytes + bytes > MAX_LOG_BYTES) {
                    logsTruncated = true;
                    return;
                }
                logs.add(line);
                logBytes += bytes;
            }
        }

        void cancel() {
            ScriptExecution active;
            boolean wasStarted;
            synchronized (this) {
                if (done) return;
                pendingStop = "canceled";
                pendingStopReason = "Execution canceled because its stream closed";
                active = execution;
                wasStarted = started;
                if (!wasStarted) done = true;
            }
            if (!wasStarted) return;
            if (active != null) forceStop(active);
            finish("canceled", null, "Execution canceled because its stream closed");
        }

        private void stopAndFinish(String status, String reason) {
            ScriptExecution active;
            synchronized (this) {
                if (done) return;
                pendingStop = status;
                pendingStopReason = reason;
                active = execution;
            }
            if (active != null) forceStop(active);
            finish(status, null, reason);
        }

        private void forceStop(ScriptExecution current) {
            ScriptEngine<?> engine = current == null ? null : current.getEngine();
            if (engine != null) {
                try { engine.forceStop(); }
                catch (Throwable e) { Log.w(TAG, "Auto.js engine stop failed", e); }
            }
        }

        private void finish(String status, Object value, String error) {
            JSONObject result;
            synchronized (this) {
                if (done) return;
                if (pendingStop != null) {
                    status = pendingStop;
                    value = null;
                    error = pendingStopReason;
                }
                done = true;
                if (console != null && logListener != null) console.removeLogListener(logListener);
                MAIN.removeCallbacks(timeout);
                result = createResult(status, value, error);
            }
            completion.onComplete(result);
        }

        private JSONObject createResult(String status, Object value, String error) {
            JSONArray logArray = new JSONArray();
            for (String line : logs) logArray.put(line);
            JSONObject result = new JSONObject();
            String text = value == null ? "" : String.valueOf(value);
            String boundedValue = bounded(text, MAX_TEXT_BYTES);
            String boundedError = error == null ? null : bounded(error, MAX_TEXT_BYTES);
            try {
                result.put("status", status);
                result.put("value", boundedValue);
                result.put("logs", logArray);
                if (boundedError != null) result.put("error", boundedError);
                if (logsTruncated || boundedValue.length() != text.length() ||
                        (error != null && boundedError.length() != error.length())) result.put("truncated", true);
            } catch (Exception ignored) { }
            return result;
        }

        private boolean isInterrupted(Throwable error) {
            for (Throwable current = error; current != null; current = current.getCause()) {
                if (current instanceof ScriptInterruptedException || current instanceof InterruptedException) return true;
            }
            return false;
        }

        private String errorText(Throwable error) {
            String message = error == null ? "Auto.js execution failed" : error.getMessage();
            return message == null || message.isEmpty() ? String.valueOf(error) : message;
        }
    }

    static String bounded(String value, int maxBytes) {
        int end = 0;
        int used = 0;
        while (end < value.length()) {
            int codePoint = value.codePointAt(end);
            int width = utf8Width(codePoint);
            if (used + width > maxBytes) break;
            used += width;
            end += Character.charCount(codePoint);
        }
        return end == value.length() ? value : value.substring(0, end);
    }

    private static int utf8Width(int codePoint) {
        if (codePoint <= 0x7f || (codePoint >= 0xd800 && codePoint <= 0xdfff)) return 1;
        return codePoint <= 0x7ff ? 2 : codePoint <= 0xffff ? 3 : 4;
    }

    private static int utf8Length(String text) {
        int bytes = 0;
        for (int i = 0; i < text.length();) {
            int codePoint = text.codePointAt(i);
            bytes += utf8Width(codePoint);
            i += Character.charCount(codePoint);
        }
        return bytes;
    }
}
