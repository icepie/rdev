package dev.icepie.rdev;

import android.util.Log;

import java.io.ByteArrayOutputStream;
import java.io.EOFException;
import java.io.IOException;
import java.net.URI;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.TimeUnit;

import io.jpower.kcp.netty.ChannelOptionHelper;
import io.jpower.kcp.netty.UkcpChannel;
import io.jpower.kcp.netty.UkcpChannelOption;
import io.jpower.kcp.netty.UkcpClientChannel;
import io.netty.bootstrap.Bootstrap;
import io.netty.buffer.ByteBuf;
import io.netty.buffer.Unpooled;
import io.netty.channel.ChannelFuture;
import io.netty.channel.ChannelHandlerContext;
import io.netty.channel.ChannelInboundHandlerAdapter;
import io.netty.channel.ChannelInitializer;
import io.netty.channel.ChannelPipeline;
import io.netty.channel.EventLoopGroup;
import io.netty.channel.nio.NioEventLoopGroup;

final class RDevKcpClient implements RDevControlConnection {
    private static final String TAG = "RDevKcp";
    private final String endpoint;
    private final Listener listener;
    private final Object writeLock = new Object();
    private final ByteQueue incoming = new ByteQueue();
    private volatile boolean running;
    private volatile UkcpChannel channel;
    private EventLoopGroup group;
    private Thread thread;
    private Thread pingThread;

    RDevKcpClient(String endpoint, Listener listener) {
        this.endpoint = normalize(endpoint);
        this.listener = listener;
    }

    @Override public void connect() {
        running = true;
        thread = new Thread(this::runLoop, "rdev-kcp");
        thread.start();
    }

    @Override public void close() {
        running = false;
        incoming.close();
        UkcpChannel ch = channel;
        channel = null;
        if (ch != null) ch.close();
        EventLoopGroup g = group;
        group = null;
        if (g != null) g.shutdownGracefully(0, 2, TimeUnit.SECONDS);
    }

    @Override public void sendText(String text) throws IOException {
        send(RDevStreamFrame.KIND_JSON, text.getBytes("UTF-8"));
    }

    @Override public void sendBinary(byte[] data) throws IOException {
        send(RDevStreamFrame.KIND_BINARY, data);
    }

    private void runLoop() {
        Exception closeError = null;
        try {
            URI uri = URI.create(endpoint);
            String host = uri.getHost();
            int port = uri.getPort();
            if (host == null || host.length() == 0 || port <= 0) throw new IOException("bad kcp endpoint: " + endpoint);
            group = new NioEventLoopGroup(1);
            CountDownLatch active = new CountDownLatch(1);
            Bootstrap bootstrap = new Bootstrap();
            bootstrap.group(group)
                .channel(UkcpClientChannel.class)
                .handler(new ChannelInitializer<UkcpChannel>() {
                    @Override public void initChannel(UkcpChannel ch) {
                        ChannelPipeline p = ch.pipeline();
                        p.addLast(new KcpInboundHandler(active));
                    }
                });
            ChannelOptionHelper.nodelay(bootstrap, true, 20, 2, true)
                .option(UkcpChannelOption.UKCP_MTU, 1200)
                .option(UkcpChannelOption.UKCP_RCV_WND, 256)
                .option(UkcpChannelOption.UKCP_SND_WND, 256)
                .option(UkcpChannelOption.UKCP_STREAM, true);
            ChannelFuture future = bootstrap.connect(host, port).sync();
            channel = (UkcpChannel) future.channel();
            if (!active.await(10, TimeUnit.SECONDS)) throw new IOException("kcp active timeout");
            Log.i(TAG, "connected " + endpoint);
            startPingLoop();
            listener.onOpen();
            while (running) {
                RDevStreamFrame frame = readFrame();
                if (frame.kind == RDevStreamFrame.KIND_JSON) listener.onText(new String(frame.payload, "UTF-8"));
                else if (frame.kind == RDevStreamFrame.KIND_BINARY) listener.onBinary(frame.payload);
                else if (frame.kind == RDevStreamFrame.KIND_PING) send(RDevStreamFrame.KIND_PONG, frame.payload);
                else if (frame.kind == RDevStreamFrame.KIND_CLOSE) break;
            }
        } catch (Exception e) {
            closeError = e;
            if (running) Log.w(TAG, "kcp closed", e);
        } finally {
            running = false;
            stopPingLoop();
            close();
            listener.onClosed(closeError);
        }
    }

    private RDevStreamFrame readFrame() throws IOException {
        byte[] header = incoming.readFully(5);
        int len = ((header[1] & 0xff) << 24) | ((header[2] & 0xff) << 16) | ((header[3] & 0xff) << 8) | (header[4] & 0xff);
        if (len < 0 || len > 16 * 1024 * 1024) throw new IOException("stream frame too large: " + len);
        return new RDevStreamFrame(header[0] & 0xff, incoming.readFully(len));
    }

    private void send(int kind, byte[] payload) throws IOException {
        if (!running || channel == null || !channel.isActive()) throw new IOException("kcp not connected");
        ByteArrayOutputStream out = new ByteArrayOutputStream(5 + (payload == null ? 0 : payload.length));
        RDevStreamFrame.write(out, kind, payload);
        synchronized (writeLock) {
            ChannelFuture f = channel.writeAndFlush(Unpooled.wrappedBuffer(out.toByteArray()));
            try { f.sync(); }
            catch (InterruptedException e) { Thread.currentThread().interrupt(); throw new IOException("kcp send interrupted", e); }
        }
    }

    private void startPingLoop() {
        stopPingLoop();
        pingThread = new Thread(() -> {
            while (running) {
                try {
                    Thread.sleep(25000);
                    if (running) send(RDevStreamFrame.KIND_PING, new byte[] {'r','d','e','v'});
                } catch (InterruptedException e) {
                    Thread.currentThread().interrupt();
                    return;
                } catch (IOException e) {
                    Log.w(TAG, "kcp ping failed", e);
                    close();
                    return;
                }
            }
        }, "rdev-kcp-ping");
        pingThread.start();
    }

    private void stopPingLoop() {
        Thread t = pingThread;
        pingThread = null;
        if (t != null) t.interrupt();
    }

    static String normalize(String raw) {
        String value = raw == null ? "" : raw.trim();
        if (value.startsWith("udp://")) value = "kcp://" + value.substring(6);
        if (!value.contains("://")) value = "kcp://" + value;
        if (value.startsWith("kcp:///")) value = "kcp://" + value.substring(7).replaceFirst("^/+", "");
        return value;
    }

    private final class KcpInboundHandler extends ChannelInboundHandlerAdapter {
        private final CountDownLatch active;
        KcpInboundHandler(CountDownLatch active) { this.active = active; }
        @Override public void channelActive(ChannelHandlerContext ctx) {
            UkcpChannel ch = (UkcpChannel) ctx.channel();
            ch.conv(0);
            channel = ch;
            active.countDown();
        }
        @Override public void channelRead(ChannelHandlerContext ctx, Object msg) {
            ByteBuf buf = (ByteBuf) msg;
            try {
                byte[] data = new byte[buf.readableBytes()];
                buf.readBytes(data);
                incoming.write(data);
            } finally {
                buf.release();
            }
        }
        @Override public void channelInactive(ChannelHandlerContext ctx) {
            incoming.close();
        }
        @Override public void exceptionCaught(ChannelHandlerContext ctx, Throwable cause) {
            Log.w(TAG, "kcp exception", cause);
            incoming.close();
            ctx.close();
        }
    }

    private static final class ByteQueue {
        private byte[] data = new byte[8192];
        private int read;
        private int write;
        private boolean closed;

        synchronized void write(byte[] bytes) {
            if (closed || bytes == null || bytes.length == 0) return;
            compactOrGrow(bytes.length);
            System.arraycopy(bytes, 0, data, write, bytes.length);
            write += bytes.length;
            notifyAll();
        }

        synchronized byte[] readFully(int len) throws IOException {
            byte[] out = new byte[len];
            int off = 0;
            while (off < len) {
                while (!closed && available() == 0) {
                    try { wait(); }
                    catch (InterruptedException e) { Thread.currentThread().interrupt(); throw new IOException("kcp read interrupted", e); }
                }
                if (available() == 0 && closed) throw new EOFException("kcp eof");
                int n = Math.min(len - off, available());
                System.arraycopy(data, read, out, off, n);
                read += n;
                off += n;
                if (read == write) { read = 0; write = 0; }
            }
            return out;
        }

        synchronized void close() {
            closed = true;
            notifyAll();
        }

        private int available() { return write - read; }

        private void compactOrGrow(int extra) {
            if (data.length - write >= extra) return;
            int available = available();
            if (read > 0 && data.length - available >= extra) {
                System.arraycopy(data, read, data, 0, available);
                read = 0;
                write = available;
                return;
            }
            int size = data.length;
            while (size - available < extra) size *= 2;
            byte[] next = new byte[size];
            System.arraycopy(data, read, next, 0, available);
            data = next;
            read = 0;
            write = available;
        }
    }
}
