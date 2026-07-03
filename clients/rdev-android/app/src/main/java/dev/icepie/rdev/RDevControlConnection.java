package dev.icepie.rdev;

import java.io.IOException;

interface RDevControlConnection {
    interface Listener {
        void onOpen();
        void onText(String text);
        void onBinary(byte[] data);
        void onClosed(Exception error);
    }

    void connect();
    void close();
    void sendText(String text) throws IOException;
    void sendBinary(byte[] data) throws IOException;
}
