package cn.singzer.rdev.android;

import android.media.MediaCodec;
import android.media.MediaCodecInfo;
import android.media.MediaCodecList;
import android.media.MediaFormat;
import android.os.Bundle;
import android.util.Log;
import android.view.Surface;

import java.nio.ByteBuffer;
import java.util.concurrent.atomic.AtomicBoolean;

final class VideoEncoderPipeline {
    private static final String TAG = "RDevEncoder";
    private static final String MIME_AVC = "video/avc";
    private final int width;
    private final int height;
    private final int fps;
    private final int bitrate;
    private MediaCodec codec;
    private Surface inputSurface;
    private Thread drainThread;
    private final AtomicBoolean running = new AtomicBoolean(false);
    private long frameCount;
    private long bytesOut;

    VideoEncoderPipeline(int width, int height, int fps, int bitrate) {
        this.width = width;
        this.height = height;
        this.fps = fps;
        this.bitrate = bitrate;
    }

    Surface start() throws Exception {
        String encoderName = chooseAvcEncoder();
        if (encoderName != null) {
            codec = MediaCodec.createByCodecName(encoderName);
        } else {
            codec = MediaCodec.createEncoderByType(MIME_AVC);
            encoderName = codec.getName();
        }
        MediaFormat format = MediaFormat.createVideoFormat(MIME_AVC, width, height);
        format.setInteger(MediaFormat.KEY_COLOR_FORMAT, MediaCodecInfo.CodecCapabilities.COLOR_FormatSurface);
        format.setInteger(MediaFormat.KEY_BIT_RATE, bitrate);
        format.setInteger(MediaFormat.KEY_FRAME_RATE, fps);
        format.setInteger(MediaFormat.KEY_I_FRAME_INTERVAL, 1);
        codec.configure(format, null, null, MediaCodec.CONFIGURE_FLAG_ENCODE);
        inputSurface = codec.createInputSurface();
        codec.start();
        AndroidVideoHub.setKeyFrameRequester(this::requestKeyFrame);
        running.set(true);
        drainThread = new Thread(this::drainLoop, "rdev-encoder-drain");
        drainThread.start();
        Log.i(TAG, "encoder started name=" + encoderName + " size=" + width + "x" + height + " fps=" + fps + " bitrate=" + bitrate);
        requestKeyFrame();
        return inputSurface;
    }

    void stop() {
        running.set(false);
        if (drainThread != null) {
            try { drainThread.join(1000); } catch (InterruptedException e) { Thread.currentThread().interrupt(); }
            drainThread = null;
        }
        if (codec != null) {
            try { codec.stop(); } catch (Throwable ignored) {}
            try { codec.release(); } catch (Throwable ignored) {}
            codec = null;
        }
        if (inputSurface != null) {
            try { inputSurface.release(); } catch (Throwable ignored) {}
            inputSurface = null;
        }
        AndroidVideoHub.setKeyFrameRequester(null);
        Log.i(TAG, "encoder stopped frames=" + frameCount + " bytes=" + bytesOut);
    }

    private void drainLoop() {
        MediaCodec.BufferInfo info = new MediaCodec.BufferInfo();
        long lastStats = System.currentTimeMillis();
        while (running.get()) {
            try {
                int index = codec.dequeueOutputBuffer(info, 10000);
                if (index == MediaCodec.INFO_TRY_AGAIN_LATER) {
                    continue;
                }
                if (index == MediaCodec.INFO_OUTPUT_FORMAT_CHANGED) {
                    MediaFormat outputFormat = codec.getOutputFormat();
                    Log.i(TAG, "output format=" + outputFormat);
                    AndroidVideoHub.publishConfig(width, height, bufferBytes(outputFormat.getByteBuffer("csd-0")), bufferBytes(outputFormat.getByteBuffer("csd-1")));
                    continue;
                }
                if (index < 0) continue;
                ByteBuffer output = codec.getOutputBuffer(index);
                int size = info.size;
                boolean keyFrame = (info.flags & MediaCodec.BUFFER_FLAG_KEY_FRAME) != 0;
                boolean config = (info.flags & MediaCodec.BUFFER_FLAG_CODEC_CONFIG) != 0;
                if (output != null && size > 0) {
                    byte[] sample = new byte[size];
                    output.position(info.offset);
                    output.limit(info.offset + size);
                    output.get(sample);
                    bytesOut += size;
                    if (!config) {
                        frameCount++;
                        AndroidVideoHub.publishSample(sample, info.presentationTimeUs, keyFrame);
                    }
                    if (keyFrame || config || frameCount <= 3) {
                        Log.i(TAG, "sample size=" + size + " flags=" + info.flags + " pts=" + info.presentationTimeUs + " key=" + keyFrame + " config=" + config);
                    }
                }
                codec.releaseOutputBuffer(index, false);
                long now = System.currentTimeMillis();
                if (now - lastStats >= 3000) {
                    Log.i(TAG, "stats frames=" + frameCount + " bytes=" + bytesOut);
                    lastStats = now;
                }
            } catch (IllegalStateException e) {
                Log.w(TAG, "encoder drain state error", e);
                return;
            } catch (Throwable t) {
                Log.e(TAG, "encoder drain error", t);
                return;
            }
        }
    }

    private byte[] bufferBytes(ByteBuffer buffer) {
        if (buffer == null) return null;
        ByteBuffer dup = buffer.duplicate();
        byte[] out = new byte[dup.remaining()];
        dup.get(out);
        return out;
    }

    void requestKeyFrame() {
        try {
            Bundle params = new Bundle();
            params.putInt(MediaCodec.PARAMETER_KEY_REQUEST_SYNC_FRAME, 0);
            codec.setParameters(params);
        } catch (Throwable t) {
            Log.w(TAG, "request key frame failed", t);
        }
    }

    private String chooseAvcEncoder() {
        String software = null;
        for (int i = 0; i < MediaCodecList.getCodecCount(); i++) {
            MediaCodecInfo info = MediaCodecList.getCodecInfoAt(i);
            if (!info.isEncoder()) continue;
            for (String type : info.getSupportedTypes()) {
                if (!MIME_AVC.equalsIgnoreCase(type)) continue;
                try {
                    MediaCodecInfo.CodecCapabilities caps = info.getCapabilitiesForType(type);
                    boolean surface = false;
                    for (int color : caps.colorFormats) {
                        if (color == MediaCodecInfo.CodecCapabilities.COLOR_FormatSurface) {
                            surface = true;
                            break;
                        }
                    }
                    if (!surface) continue;
                    String name = info.getName();
                    String lower = name.toLowerCase();
                    if (!lower.contains("google") && !lower.contains("android")) return name;
                    if (software == null) software = name;
                } catch (Throwable ignored) {}
            }
        }
        return software;
    }
}
