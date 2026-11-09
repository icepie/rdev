package dev.icepie.rdev;

import org.junit.After;
import org.junit.Test;
import static org.junit.Assert.*;

public class AgentScreenshotTest {
    @After public void cleanup() { AndroidVideoHub.setScreenshotProvider(null); }

    @Test public void missingPermissionReturnsErrorWithoutStartingVideo() {
        AndroidVideoHub.setScreenshotProvider(null);
        AndroidVideoHub.screenshot((png, error) -> {
            assertNull(png);
            assertTrue(error.contains("permission"));
        });
        assertFalse(AndroidVideoHub.hasListeners());
    }

    @Test public void snapshotsUseProviderWithoutVideoSubscription() {
        int[] captures = {0};
        AndroidVideoHub.setScreenshotProvider(callback -> {
            captures[0]++;
            callback.complete("image", null);
        });
        for (int i=0; i<2; i++) {
            AndroidVideoHub.screenshot((png, error) -> { assertEquals("image", png); assertNull(error); });
        }
        assertEquals(2, captures[0]);
        assertFalse(AndroidVideoHub.hasListeners());
    }
}
