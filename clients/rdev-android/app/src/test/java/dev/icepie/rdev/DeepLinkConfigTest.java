package dev.icepie.rdev;

import org.junit.Test;
import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertFalse;
import static org.junit.Assert.assertNull;
import static org.junit.Assert.assertTrue;

public class DeepLinkConfigTest {
    @Test public void parsesAndDecodesConnectLink() {
        MainActivity.DeepLinkConfig config = MainActivity.DeepLinkConfig.parse(
            "rdev://connect?server=tcp%3A%2F%2Fr.feidu.fit%3A8481%2Cwss%3A%2F%2Fr.feidu.fit&id=phone-01&password=a%26b"
        );
        assertEquals("tcp://r.feidu.fit:8481,wss://r.feidu.fit", config.server);
        assertEquals("phone-01", config.id);
        assertEquals("a&b", config.password);
        assertTrue(config.connect);
    }

    @Test public void acceptsShortAliasesAndFillAction() {
        MainActivity.DeepLinkConfig config = MainActivity.DeepLinkConfig.parse(
            "rdev://connect?s=wss%3A%2F%2Fr.feidu.fit&device=phone-02&p=&action=fill"
        );
        assertEquals("wss://r.feidu.fit", config.server);
        assertEquals("phone-02", config.id);
        assertEquals("", config.password);
        assertFalse(config.connect);
    }

    @Test public void rejectsIncompleteOrUnrelatedLinks() {
        assertNull(MainActivity.DeepLinkConfig.parse("rdev://connect?server=wss%3A%2F%2Fr.feidu.fit"));
        assertNull(MainActivity.DeepLinkConfig.parse("rdev://other?server=x&id=y"));
        assertNull(MainActivity.DeepLinkConfig.parse("https://connect?server=x&id=y"));
    }
}
