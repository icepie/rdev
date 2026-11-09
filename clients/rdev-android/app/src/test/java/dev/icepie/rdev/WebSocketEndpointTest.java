package dev.icepie.rdev;

import org.junit.Test;
import static org.junit.Assert.assertEquals;

public class WebSocketEndpointTest {
    @Test public void serverRootsUseControlEndpoint() {
        assertEquals("wss://example.com/ws", RDevWebSocketClient.normalizeUrl("https://example.com/"));
        assertEquals("ws://example.com:8080/ws", RDevWebSocketClient.normalizeUrl("example.com:8080"));
        assertEquals("wss://example.com/ws?token=a%26b", RDevWebSocketClient.normalizeUrl("wss://example.com?token=a%26b"));
    }

    @Test public void tunnelPathAndEncodedCredentialsArePreserved() {
        String endpoint = "wss://example.com/gpu-desktop-tunnel?device=phone%20one&instanceId=abc&password=a%26b%2Bc%25";
        assertEquals(endpoint, RDevWebSocketClient.normalizeUrl(endpoint));
    }

    @Test public void explicitControlPathsArePreserved() {
        assertEquals("wss://example.com/prefix/ws?token=a%2Fb", RDevWebSocketClient.normalizeUrl("https://example.com/prefix/ws?token=a%2Fb"));
    }
}
