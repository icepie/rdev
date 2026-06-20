package cn.singzer.rdev.android;

import android.accessibilityservice.AccessibilityService;
import android.accessibilityservice.GestureDescription;
import android.graphics.Path;
import android.os.Build;
import android.util.Log;
import android.view.accessibility.AccessibilityEvent;

public class RDevAccessibilityService extends AccessibilityService {
    private static final String TAG = "RDevInput";
    private static volatile RDevAccessibilityService instance;

    static boolean isActive() { return instance != null; }

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
