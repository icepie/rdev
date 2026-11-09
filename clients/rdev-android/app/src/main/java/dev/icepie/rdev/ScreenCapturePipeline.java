package dev.icepie.rdev;

import android.content.Context;
import android.graphics.Bitmap;
import android.graphics.PixelFormat;
import android.media.Image;
import android.media.ImageReader;
import android.util.Base64;
import java.io.ByteArrayOutputStream;
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
    private volatile Handler handler;
    private VideoEncoderPipeline encoder;
    private Surface videoSurface;
    private ImageReader screenshotReader;
    private AndroidVideoHub.ScreenshotCallback screenshotCallback;
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

    private synchronized Handler worker() {
        if (projectionStopped) return null;
        if (handler == null) {
            thread = new HandlerThread("rdev-capture");
            thread.start();
            handler = new Handler(thread.getLooper());
        }
        return handler;
    }

    synchronized void start() {
        if (running || projectionStopped) return;
        running = true;
        Handler h = worker();
        if (h != null) h.post(this::startOnThread);
    }

    void stop() {
        Handler h = handler;
        if (h != null) h.post(() -> stopOnThread(false));
    }

    void releaseProjection() {
        Handler h = worker();
        if (h != null) h.post(() -> stopOnThread(true));
    }

    void screenshot(AndroidVideoHub.ScreenshotCallback callback) {
        Handler h = worker();
        if (h == null) { callback.complete(null, "Screen sharing permission has expired"); return; }
        h.post(() -> screenshotOnThread(callback));
    }

    private void ensureDisplay(CaptureConfig config) {
        if (virtualDisplay != null) return;
        width = config.width;
        height = config.height;
        projectionCallback = new MediaProjection.Callback() {
            @Override public void onStop() { stopOnThread(true); }
        };
        projection.registerCallback(projectionCallback, handler);
        // One display per projection grant, reused by video and on-demand capture.
        virtualDisplay = projection.createVirtualDisplay("RDev Android", width, height,
            config.densityDpi, DisplayManager.VIRTUAL_DISPLAY_FLAG_AUTO_MIRROR, null, null, handler);
    }

    private void screenshotOnThread(AndroidVideoHub.ScreenshotCallback callback) {
        if (projectionStopped) { callback.complete(null, "Screen sharing permission has expired"); return; }
        if (screenshotCallback != null) { callback.complete(null, "A screenshot is already in progress"); return; }
        screenshotCallback = callback;
        try {
            ensureDisplay(chooseConfig());
            screenshotReader = ImageReader.newInstance(width, height, PixelFormat.RGBA_8888, 2);
            screenshotReader.setOnImageAvailableListener(reader -> {
                if (reader != screenshotReader) return;
                try (Image image = reader.acquireLatestImage()) {
                    if (image == null) return;
                    Image.Plane plane = image.getPlanes()[0];
                    int stride = plane.getRowStride() / plane.getPixelStride();
                    Bitmap padded = Bitmap.createBitmap(stride, height, Bitmap.Config.ARGB_8888);
                    Bitmap cropped = null;
                    Bitmap scaled = null;
                    try {
                        padded.copyPixelsFromBuffer(plane.getBuffer());
                        cropped = Bitmap.createBitmap(padded, 0, 0, width, height);
                        float scale = Math.min(1f, Math.min(1280f / width, 720f / height));
                        scaled = Bitmap.createScaledBitmap(cropped, Math.max(1, Math.round(width * scale)), Math.max(1, Math.round(height * scale)), true);
                        ByteArrayOutputStream output = new ByteArrayOutputStream();
                        if (!scaled.compress(Bitmap.CompressFormat.PNG, 100, output)) throw new IllegalStateException("PNG encoding failed");
                        finishScreenshot(Base64.encodeToString(output.toByteArray(), Base64.NO_WRAP), null);
                    } finally {
                        if (scaled != null && scaled != cropped && scaled != padded) scaled.recycle();
                        if (cropped != null && cropped != padded) cropped.recycle();
                        padded.recycle();
                    }
                } catch (Exception e) { finishScreenshot(null, e.toString()); }
            }, handler);
            virtualDisplay.setSurface(screenshotReader.getSurface());
            ImageReader expected = screenshotReader;
            handler.postDelayed(() -> {
                if (screenshotReader == expected) finishScreenshot(null, "Screenshot timed out; check screen sharing permission");
            }, 5000);
        } catch (Exception e) { finishScreenshot(null, e.toString()); }
    }

    private void finishScreenshot(String png, String error) {
        AndroidVideoHub.ScreenshotCallback callback = screenshotCallback;
        screenshotCallback = null;
        try { if (virtualDisplay != null) virtualDisplay.setSurface(videoSurface); } catch (Exception ignored) {}
        if (screenshotReader != null) { screenshotReader.close(); screenshotReader = null; }
        if (videoSurface != null) AndroidVideoHub.requestKeyFrame();
        if (callback != null) callback.complete(png, error);
    }

    boolean isRunning() { return running; }

    int width() { return width; }

    int height() { return height; }

    private void startOnThread() {
        try {
            CaptureConfig config = chooseConfig();
            ensureDisplay(config);
            encoder = new VideoEncoderPipeline(width, height, config.fps, config.bitrate);
            videoSurface = encoder.start();
            if (screenshotReader == null) virtualDisplay.setSurface(videoSurface);
            Log.i(TAG, "capture started " + config.width + "x" + config.height + " dpi=" + config.densityDpi + " fps=" + config.fps + " bitrate=" + config.bitrate);
        } catch (Throwable t) {
            Log.e(TAG, "capture start failed", t);
            stopOnThread(false);
        }
    }

    private void stopOnThread(boolean releaseProjection) {
        // Idle video detaches its surface without discarding the projection or
        // restarting an already open viewer when an Agent screenshot is requested.
        if (releaseProjection && projectionStopped) return;
        if (releaseProjection) projectionStopped = true;
        videoSurface = null;
        try {
            if (screenshotReader == null && virtualDisplay != null) virtualDisplay.setSurface(null);
            if (encoder != null) { encoder.stop(); encoder = null; }
            if (releaseProjection) {
                finishScreenshot(null, "Screen sharing stopped");
                if (virtualDisplay != null) { virtualDisplay.release(); virtualDisplay = null; }
                if (projectionCallback != null) { projection.unregisterCallback(projectionCallback); projectionCallback = null; }
                projection.stop();
            }
        } catch (Exception e) { Log.w(TAG, "capture stop error", e); }
        running = false;
        AndroidVideoHub.clearVideoState();
        if (releaseProjection && thread != null) {
            thread.quitSafely();
            thread = null;
            handler = null;
        }
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
