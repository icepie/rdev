package cn.singzer.rdev.android;

import java.util.ArrayList;
import java.util.List;

final class AndroidVideoHub {
    interface Listener {
        void onVideoConfig(int width, int height, byte[] sps, byte[] pps);
        void onVideoSample(byte[] data, long ptsUs, boolean keyFrame);
    }

    private static final List<Listener> listeners = new ArrayList<>();
    private static int width;
    private static int height;
    private static byte[] sps;
    private static byte[] pps;
    private static KeyFrameRequester keyFrameRequester;

    interface KeyFrameRequester {
        void requestKeyFrame();
    }

    static synchronized void setKeyFrameRequester(KeyFrameRequester requester) {
        keyFrameRequester = requester;
    }

    static synchronized void requestKeyFrame() {
        if (keyFrameRequester != null) keyFrameRequester.requestKeyFrame();
    }

    static synchronized void addListener(Listener listener) {
        listeners.add(listener);
        if (sps != null && pps != null) listener.onVideoConfig(width, height, sps, pps);
    }

    static synchronized void removeListener(Listener listener) {
        listeners.remove(listener);
    }

    static synchronized void publishConfig(int w, int h, byte[] config0, byte[] config1) {
        width = w;
        height = h;
        sps = copy(config0);
        pps = copy(config1);
        for (Listener listener : new ArrayList<>(listeners)) listener.onVideoConfig(width, height, sps, pps);
    }

    static synchronized void publishSample(byte[] data, long ptsUs, boolean keyFrame) {
        for (Listener listener : new ArrayList<>(listeners)) listener.onVideoSample(copy(data), ptsUs, keyFrame);
    }

    private static byte[] copy(byte[] data) {
        if (data == null) return null;
        byte[] out = new byte[data.length];
        System.arraycopy(data, 0, out, 0, data.length);
        return out;
    }
}
