package dev.icepie.rdev;

import org.json.JSONArray;
import org.json.JSONObject;

import java.util.ArrayDeque;
import java.util.ArrayList;
import java.util.Date;
import java.text.SimpleDateFormat;
import java.util.Locale;
import java.util.TimeZone;

final class RDevLogCollector {
    private final ArrayDeque<JSONObject> ring = new ArrayDeque<>();
    private boolean enabled = false;
    private String level = "info";
    private int maxLineBytes = 8192;
    private boolean flushing = false;
    private RDevAgentService service;

    synchronized void attach(RDevAgentService service) { this.service = service; }
    synchronized void configure(JSONObject config) {
        enabled = config.optBoolean("logEnabled", false);
        level = normalize(config.optString("logLevel", "info"));
        maxLineBytes = Math.max(256, config.optInt("maxLineBytes", 8192));
        if (enabled) flushLocked(new ArrayList<>(ring));
    }
    void i(String tag, String msg) { record("info", tag, msg, null); }
    void w(String tag, String msg, Throwable t) { record("warn", tag, msg + (t == null ? "" : " " + t.getClass().getSimpleName() + ": " + t.getMessage()), null); }
    synchronized void record(String lvl, String tag, String msg, JSONObject fields) {
        try {
            String text = sanitize(msg == null ? "" : msg);
            if (text.length() > maxLineBytes) text = text.substring(0, maxLineBytes);
            JSONObject e = new JSONObject().put("ts", now()).put("level", normalize(lvl)).put("module", tag == null ? "android" : tag).put("msg", text);
            if (fields != null) e.put("fields", fields);
            ring.addLast(e);
            while (ring.size() > 1000) ring.removeFirst();
            if (enabled && !flushing && rank(lvl) >= rank(level)) flushLocked(java.util.Collections.singletonList(e));
        } catch (Exception ignored) {}
    }
    private void flushLocked(java.util.List<JSONObject> entries) {
        if (service == null || entries == null || entries.isEmpty() || flushing) return;
        try {
            JSONArray arr = new JSONArray();
            for (JSONObject e : entries) if (rank(e.optString("level", "info")) >= rank(level)) arr.put(e);
            if (arr.length() == 0) return;
            flushing = true;
            service.sendText(new JSONObject().put("type", "log_batch").put("logs", arr));
        } catch (Exception ignored) {
        } finally {
            flushing = false;
        }
    }
    private static String normalize(String l) {
        String v = l == null ? "" : l.trim().toLowerCase(Locale.US);
        if ("trace".equals(v)||"debug".equals(v)||"info".equals(v)||"warn".equals(v)||"error".equals(v)) return v;
        return "info";
    }
    private static int rank(String l) { String v = normalize(l); if ("trace".equals(v)) return 0; if ("debug".equals(v)) return 1; if ("warn".equals(v)) return 3; if ("error".equals(v)) return 4; return 2; }
    private static String sanitize(String s) { return s.replaceAll("(?i)(password|token|authorization|cookie|secret|key)=\\S+", "$1=[redacted]"); }
    private static String now() { SimpleDateFormat f = new SimpleDateFormat("yyyy-MM-dd'T'HH:mm:ss.SSS'Z'", Locale.US); f.setTimeZone(TimeZone.getTimeZone("UTC")); return f.format(new Date()); }
}
