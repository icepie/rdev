package dev.icepie.rdev;

import java.util.ArrayList;
import java.util.List;

final class AndroidVideoHub {
    interface Listener {
        void onVideoConfig(int width, int height, byte[] sps, byte[] pps);
        void onVideoSample(byte[] data, long ptsUs, boolean keyFrame);
    }

    interface DemandListener {
        void onVideoDemandChanged(boolean active);
    }

    private static final List<Listener> listeners = new ArrayList<>();
    private static int width;
    private static int height;
    private static byte[] sps;
    private static byte[] pps;
    private static KeyFrameRequester keyFrameRequester;
    private static DemandListener demandListener;

    interface KeyFrameRequester {
        void requestKeyFrame();
    }

    static synchronized void setKeyFrameRequester(KeyFrameRequester requester) {
        keyFrameRequester = requester;
    }

    static synchronized void setDemandListener(DemandListener listener) {
        demandListener = listener;
        if (demandListener != null) demandListener.onVideoDemandChanged(!listeners.isEmpty());
    }

    static synchronized void requestKeyFrame() {
        if (keyFrameRequester != null) keyFrameRequester.requestKeyFrame();
    }

    static synchronized boolean hasListeners() {
        return !listeners.isEmpty();
    }

    static synchronized void addListener(Listener listener) {
        boolean becameActive = listeners.isEmpty();
        listeners.add(listener);
        if (becameActive && demandListener != null) demandListener.onVideoDemandChanged(true);
        if (sps != null && pps != null) listener.onVideoConfig(width, height, sps, pps);
    }

    static synchronized void removeListener(Listener listener) {
        boolean removed = listeners.remove(listener);
        if (removed && listeners.isEmpty() && demandListener != null) demandListener.onVideoDemandChanged(false);
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

    static synchronized void clearVideoState() {
        width = 0;
        height = 0;
        sps = null;
        pps = null;
        keyFrameRequester = null;
    }

    private static byte[] copy(byte[] data) {
        if (data == null) return null;
        byte[] out = new byte[data.length];
        System.arraycopy(data, 0, out, 0, data.length);
        return out;
    }
}
