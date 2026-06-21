package dev.icepie.rdev;

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
    private final Context context;
    private final MediaProjection projection;
    private HandlerThread thread;
    private Handler handler;
    private VideoEncoderPipeline encoder;
    private VirtualDisplay virtualDisplay;
    private MediaProjection.Callback projectionCallback;
    private volatile int width;
    private volatile int height;
    private volatile boolean running;
    private volatile boolean projectionStopped;

    ScreenCapturePipeline(Context context, MediaProjection projection) {
        this.context = context.getApplicationContext();
        this.projection = projection;
    }

    synchronized void start() {
        if (running || projectionStopped) return;
        running = true;
        thread = new HandlerThread("rdev-capture");
        thread.start();
        handler = new Handler(thread.getLooper());
        handler.post(this::startOnThread);
    }

    void stop() {
        Handler h = handler;
        if (h != null) h.post(() -> stopOnThread(false));
    }

    void releaseProjection() {
        Handler h = handler;
        if (h != null) {
            h.post(() -> stopOnThread(true));
        } else if (!projectionStopped) {
            try { projection.stop(); } catch (Throwable ignored) {}
            projectionStopped = true;
            AndroidVideoHub.clearVideoState();
        }
    }

    boolean isRunning() { return running; }

    int width() { return width; }

    int height() { return height; }

    private void startOnThread() {
        try {
            CaptureConfig config = chooseConfig();
            width = config.width;
            height = config.height;
            encoder = new VideoEncoderPipeline(config.width, config.height, config.fps, config.bitrate);
            Surface surface = encoder.start();
            virtualDisplay = projection.createVirtualDisplay(
                "RDev Android",
                config.width,
                config.height,
                config.densityDpi,
                DisplayManager.VIRTUAL_DISPLAY_FLAG_AUTO_MIRROR,
                surface,
                null,
                handler
            );
            projectionCallback = new MediaProjection.Callback() {
                @Override public void onStop() { stopOnThread(true); }
            };
            projection.registerCallback(projectionCallback, handler);
            Log.i(TAG, "capture started " + config.width + "x" + config.height + " dpi=" + config.densityDpi + " fps=" + config.fps + " bitrate=" + config.bitrate);
        } catch (Throwable t) {
            Log.e(TAG, "capture start failed", t);
            stopOnThread(false);
        }
    }

    private void stopOnThread(boolean releaseProjection) {
        try {
            if (virtualDisplay != null) {
                virtualDisplay.release();
                virtualDisplay = null;
            }
            if (encoder != null) {
                encoder.stop();
                encoder = null;
            }
            if (projectionCallback != null) {
                try { projection.unregisterCallback(projectionCallback); } catch (Throwable ignored) {}
                projectionCallback = null;
            }
            if (releaseProjection && !projectionStopped) {
                projection.stop();
                projectionStopped = true;
            }
        } catch (Throwable t) {
            Log.w(TAG, "capture stop error", t);
        }
        running = false;
        width = 0;
        height = 0;
        AndroidVideoHub.clearVideoState();
        if (thread != null) {
            thread.quitSafely();
            thread = null;
            handler = null;
        }
        Log.i(TAG, releaseProjection ? "capture released" : "capture paused");
    }

    private CaptureConfig chooseConfig() {
        DisplayMetrics metrics = new DisplayMetrics();
        WindowManager wm = (WindowManager) context.getSystemService(Context.WINDOW_SERVICE);
        wm.getDefaultDisplay().getRealMetrics(metrics);
        VideoEncoderPipeline.EncoderLimits limits = VideoEncoderPipeline.avcEncoderLimits();
        int screenWidth = metrics.widthPixels;
        int screenHeight = metrics.heightPixels;
        int maxWidth = Math.max(16, limits.maxWidth);
        int maxHeight = Math.max(16, limits.maxHeight);
        int safeMaxShort = 480;
        int safeMaxLong = 960;
        if (screenWidth > screenHeight) {
            safeMaxShort = 960;
            safeMaxLong = 480;
        }
        maxWidth = Math.min(maxWidth, safeMaxShort);
        maxHeight = Math.min(maxHeight, safeMaxLong);
        float scale = Math.min(1.0f, Math.min(maxWidth / (float) screenWidth, maxHeight / (float) screenHeight));
        int width = align16(Math.max(16, Math.round(screenWidth * scale)));
        int height = align16(Math.max(16, Math.round(screenHeight * scale)));
        int pixels = Math.max(1, width * height);
        int fps = Math.max(10, Math.min(15, limits.maxFps));
        int bitrate = Math.max(8_000_000, Math.min(limits.maxBitrate, pixels * fps));
        Log.i(TAG, "encoder limits max=" + limits.maxWidth + "x" + limits.maxHeight + " fps=" + limits.maxFps + " bitrate=" + limits.maxBitrate);
        return new CaptureConfig(width, height, metrics.densityDpi, fps, bitrate);
    }

    private int align16(int value) {
        return Math.max(16, (value / 16) * 16);
    }

    private static final class CaptureConfig {
        final int width;
        final int height;
        final int densityDpi;
        final int fps;
        final int bitrate;
        CaptureConfig(int width, int height, int densityDpi, int fps, int bitrate) {
            this.width = width;
            this.height = height;
            this.densityDpi = densityDpi;
            this.fps = fps;
            this.bitrate = bitrate;
        }
    }
}
