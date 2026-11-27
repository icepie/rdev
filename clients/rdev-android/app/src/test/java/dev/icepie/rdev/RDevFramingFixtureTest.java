package dev.icepie.rdev;

import org.junit.Test;

/** Runs the shared byte corpus (docs/protocol-framing.md) under the Android unit tests. */
public class RDevFramingFixtureTest {
    @Test public void sharedCorpusDecodesIdentically() throws Exception {
        FramingFixtures.checkStreamFrames();
        FramingFixtures.checkBinaryFrames();
    }

    @Test public void encodersRoundTripLargePayloads() throws Exception {
        FramingFixtures.checkLargePayloadRoundTrips();
    }
}
