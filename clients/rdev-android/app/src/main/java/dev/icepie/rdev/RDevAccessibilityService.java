package dev.icepie.rdev;

import android.accessibilityservice.AccessibilityService;
import android.accessibilityservice.GestureDescription;
import android.content.Context;
import android.graphics.Path;
import android.os.Build;
import android.os.Bundle;
import android.util.DisplayMetrics;
import android.util.Log;
import android.view.WindowManager;
import android.view.accessibility.AccessibilityEvent;
import android.view.accessibility.AccessibilityNodeInfo;

public class RDevAccessibilityService extends AccessibilityService {
    private static final String TAG = "RDevInput";
    private static volatile RDevAccessibilityService instance;

    static boolean isActive() { return instance != null; }

    static boolean tapNormalized(double x, double y) {
        Point point = normalizedPoint(x, y);
        return point != null && tap(point.x, point.y);
    }

    static boolean swipeNormalized(double startX, double startY, double endX, double endY, long durationMs) {
        Point start = normalizedPoint(startX, startY);
        Point end = normalizedPoint(endX, endY);
        return start != null && end != null && swipe(start.x, start.y, end.x, end.y, durationMs);
    }

    static boolean multiSwipeNormalized(double[] startX, double[] startY, double[] endX, double[] endY, long durationMs) {
        RDevAccessibilityService service = instance;
        if (service == null || Build.VERSION.SDK_INT < 24 || startX == null || startY == null || endX == null || endY == null) return false;
        int count = Math.min(Math.min(startX.length, startY.length), Math.min(endX.length, endY.length));
        if (count == 0) return false;
        GestureDescription.Builder builder = new GestureDescription.Builder();
        long duration = Math.max(80, Math.min(1000, durationMs));
        for (int i = 0; i < count; i++) {
            Point start = normalizedPoint(startX[i], startY[i]);
            Point end = normalizedPoint(endX[i], endY[i]);
            if (start == null || end == null) return false;
            Path path = new Path();
            path.moveTo(start.x, start.y);
            path.lineTo(end.x, end.y);
            builder.addStroke(new GestureDescription.StrokeDescription(path, 0, duration));
        }
        return service.dispatchGesture(builder.build(), null, null);
    }

    static boolean tap(float x, float y) {
        RDevAccessibilityService service = instance;
        if (service == null || Build.VERSION.SDK_INT < 24) return false;
        Path path = new Path();
        path.moveTo(x, y);
        GestureDescription gesture = new GestureDescription.Builder()
            .addStroke(new GestureDescription.StrokeDescription(path, 0, 80))
            .build();
        return service.dispatchGesture(gesture, null, null);
    }

    static boolean swipe(float startX, float startY, float endX, float endY, long durationMs) {
        RDevAccessibilityService service = instance;
        if (service == null || Build.VERSION.SDK_INT < 24) return false;
        Path path = new Path();
        path.moveTo(startX, startY);
        path.lineTo(endX, endY);
        long duration = Math.max(80, Math.min(1000, durationMs));
        GestureDescription gesture = new GestureDescription.Builder()
            .addStroke(new GestureDescription.StrokeDescription(path, 0, duration))
            .build();
        return service.dispatchGesture(gesture, null, null);
    }

    static boolean inputText(String text) {
        RDevAccessibilityService service = instance;
        if (service == null || text == null || text.length() == 0) return false;
        AccessibilityNodeInfo node = service.focusedInput();
        if (node == null) return false;
        CharSequence current = node.getText();
        return service.setNodeText(node, (current == null ? "" : current.toString()) + text);
    }

    static boolean backspace() {
        RDevAccessibilityService service = instance;
        if (service == null) return false;
        AccessibilityNodeInfo node = service.focusedInput();
        if (node == null) return false;
        CharSequence current = node.getText();
        if (current == null || current.length() == 0) return false;
        return service.setNodeText(node, current.subSequence(0, current.length() - 1).toString());
    }

    static boolean globalBack() {
        RDevAccessibilityService service = instance;
        return service != null && service.performGlobalAction(GLOBAL_ACTION_BACK);
    }

    private AccessibilityNodeInfo focusedInput() {
        AccessibilityNodeInfo root = getRootInActiveWindow();
        if (root == null) return null;
        AccessibilityNodeInfo focused = root.findFocus(AccessibilityNodeInfo.FOCUS_INPUT);
        if (focused != null) return focused;
        AccessibilityNodeInfo accessibilityFocus = root.findFocus(AccessibilityNodeInfo.FOCUS_ACCESSIBILITY);
        if (accessibilityFocus != null && accessibilityFocus.isEditable()) return accessibilityFocus;
        return null;
    }

    private boolean setNodeText(AccessibilityNodeInfo node, String text) {
        Bundle args = new Bundle();
        args.putCharSequence(AccessibilityNodeInfo.ACTION_ARGUMENT_SET_TEXT_CHARSEQUENCE, text);
        return node.performAction(AccessibilityNodeInfo.ACTION_SET_TEXT, args);
    }

    private static Point normalizedPoint(double x, double y) {
        RDevAccessibilityService service = instance;
        if (service == null) return null;
        DisplayMetrics metrics = new DisplayMetrics();
        WindowManager wm = (WindowManager) service.getSystemService(Context.WINDOW_SERVICE);
        wm.getDefaultDisplay().getRealMetrics(metrics);
        float px = (float) (Math.max(0, Math.min(1, x)) * metrics.widthPixels);
        float py = (float) (Math.max(0, Math.min(1, y)) * metrics.heightPixels);
        return new Point(px, py);
    }

    private static final class Point {
        final float x;
        final float y;
        Point(float x, float y) { this.x = x; this.y = y; }
    }

    @Override protected void onServiceConnected() {
        instance = this;
        Log.i(TAG, "accessibility connected");
    }

    @Override public void onDestroy() {
        if (instance == this) instance = null;
        super.onDestroy();
    }

    @Override public void onAccessibilityEvent(AccessibilityEvent event) {}

    @Override public void onInterrupt() {
        Log.i(TAG, "accessibility interrupted");
    }
}
