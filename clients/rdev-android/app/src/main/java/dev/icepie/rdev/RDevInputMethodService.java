package dev.icepie.rdev;

import android.inputmethodservice.InputMethodService;
import android.util.Log;
import android.view.KeyEvent;
import android.view.inputmethod.InputConnection;

public class RDevInputMethodService extends InputMethodService {
    private static final String TAG = "RDevIME";
    private static volatile RDevInputMethodService instance;

    static boolean isActive() {
        RDevInputMethodService service = instance;
        return service != null && service.getCurrentInputConnection() != null;
    }

    static boolean commitText(String text) {
        RDevInputMethodService service = instance;
        if (service == null || text == null || text.length() == 0) return false;
        InputConnection connection = service.getCurrentInputConnection();
        return connection != null && connection.commitText(text, 1);
    }

    static boolean deleteBackward() {
        RDevInputMethodService service = instance;
        if (service == null) return false;
        InputConnection connection = service.getCurrentInputConnection();
        return connection != null && connection.deleteSurroundingText(1, 0);
    }

    static boolean sendKey(int keyCode) {
        RDevInputMethodService service = instance;
        if (service == null) return false;
        InputConnection connection = service.getCurrentInputConnection();
        if (connection == null) return false;
        long now = System.currentTimeMillis();
        boolean down = connection.sendKeyEvent(new KeyEvent(now, now, KeyEvent.ACTION_DOWN, keyCode, 0));
        boolean up = connection.sendKeyEvent(new KeyEvent(now, now, KeyEvent.ACTION_UP, keyCode, 0));
        return down && up;
    }

    @Override public void onCreate() {
        super.onCreate();
        instance = this;
        Log.i(TAG, "ime created");
    }

    @Override public void onDestroy() {
        if (instance == this) instance = null;
        super.onDestroy();
    }
}
