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
            projection.registerCallback(new MediaProjection.Callback() {
                @Override public void onStop() { stopOnThread(); }
            }, handler);
            Log.i(TAG, "capture started " + config.width + "x" + config.height + " dpi=" + config.densityDpi + " fps=" + config.fps + " bitrate=" + config.bitrate);
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

    private CaptureConfig chooseConfig() {
        DisplayMetrics metrics = new DisplayMetrics();
        WindowManager wm = (WindowManager) context.getSystemService(Context.WINDOW_SERVICE);
        wm.getDefaultDisplay().getRealMetrics(metrics);
        VideoEncoderPipeline.EncoderLimits limits = VideoEncoderPipeline.avcEncoderLimits();
        int screenWidth = metrics.widthPixels;
        int screenHeight = metrics.heightPixels;
        int maxWidth = Math.max(16, limits.maxWidth);
        int maxHeight = Math.max(16, limits.maxHeight);
        float scale = Math.min(1.0f, Math.min(maxWidth / (float) screenWidth, maxHeight / (float) screenHeight));
        int width = align16(Math.max(16, Math.round(screenWidth * scale)));
        int height = align16(Math.max(16, Math.round(screenHeight * scale)));
        int pixels = Math.max(1, width * height);
        int fps = Math.max(15, Math.min(60, limits.maxFps));
        int bitrate = Math.max(2_000_000, Math.min(limits.maxBitrate, pixels * fps / 8));
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
