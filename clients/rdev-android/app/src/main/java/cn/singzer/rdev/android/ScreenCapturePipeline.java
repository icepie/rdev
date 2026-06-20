package cn.singzer.rdev.android;

import android.content.Context;
import android.hardware.display.DisplayManager;
import android.hardware.display.VirtualDisplay;
import android.media.projection.MediaProjection;
import android.os.Handler;
import android.os.HandlerThread;
import android.util.DisplayMetrics;
import android.util.Log;
import android.view.Surface;
import android.view.WindowManager;

final class ScreenCapturePipeline {
    private static final String TAG = "RDevCapture";
    private static final int MAX_WIDTH = 1280;
    private static final int MAX_HEIGHT = 720;

    private final Context context;
    private final MediaProjection projection;
    private HandlerThread thread;
    private Handler handler;
    private VideoEncoderPipeline encoder;
    private VirtualDisplay virtualDisplay;
    private volatile int width;
    private volatile int height;

    ScreenCapturePipeline(Context context, MediaProjection projection) {
        this.context = context.getApplicationContext();
        this.projection = projection;
    }

    void start() {
        thread = new HandlerThread("rdev-capture");
        thread.start();
        handler = new Handler(thread.getLooper());
        handler.post(this::startOnThread);
    }

    void stop() {
        if (handler != null) handler.post(this::stopOnThread);
    }

    int width() { return width; }

    int height() { return height; }

    private void startOnThread() {
        try {
            CaptureSize size = chooseSize();
            width = size.width;
            height = size.height;
            encoder = new VideoEncoderPipeline(size.width, size.height, 15, 900_000);
            Surface surface = encoder.start();
            virtualDisplay = projection.createVirtualDisplay(
                "RDev Android",
                size.width,
                size.height,
                size.densityDpi,
                DisplayManager.VIRTUAL_DISPLAY_FLAG_AUTO_MIRROR,
                surface,
                null,
                handler
            );
            projection.registerCallback(new MediaProjection.Callback() {
                @Override public void onStop() { stopOnThread(); }
            }, handler);
            Log.i(TAG, "capture started " + size.width + "x" + size.height + " dpi=" + size.densityDpi);
        } catch (Throwable t) {
            Log.e(TAG, "capture start failed", t);
            stopOnThread();
        }
    }

    private void stopOnThread() {
        try {
            if (virtualDisplay != null) {
                virtualDisplay.release();
                virtualDisplay = null;
            }
            if (encoder != null) {
                encoder.stop();
                encoder = null;
            }
            projection.stop();
        } catch (Throwable t) {
            Log.w(TAG, "capture stop error", t);
        }
        if (thread != null) {
            thread.quitSafely();
            thread = null;
            handler = null;
        }
        Log.i(TAG, "capture stopped");
    }

    private CaptureSize chooseSize() {
        DisplayMetrics metrics = new DisplayMetrics();
        WindowManager wm = (WindowManager) context.getSystemService(Context.WINDOW_SERVICE);
        wm.getDefaultDisplay().getRealMetrics(metrics);
        int width = metrics.widthPixels;
        int height = metrics.heightPixels;
        float scale = Math.min(1.0f, Math.min(MAX_WIDTH / (float) width, MAX_HEIGHT / (float) height));
        width = align16(Math.max(16, Math.round(width * scale)));
        height = align16(Math.max(16, Math.round(height * scale)));
        return new CaptureSize(width, height, metrics.densityDpi);
    }

    private int align16(int value) {
        return Math.max(16, (value / 16) * 16);
    }

    private static final class CaptureSize {
        final int width;
        final int height;
        final int densityDpi;
        CaptureSize(int width, int height, int densityDpi) {
            this.width = width;
            this.height = height;
            this.densityDpi = densityDpi;
        }
    }
}
