# Native GameStream desktop client

This Qt desktop client connects directly to Sunshine/Apollo using the GameStream protocol. It does not register with an RDev server or publish screenshots. It supports host pairing, app discovery and launch, H.264/HEVC video, audio, keyboard/mouse input, and SDL2 gamepads.

Build dependencies: CMake, Qt 6 (Core, Network, Widgets), OpenSSL, FFmpeg (libavcodec/libavutil/libswscale), SDL2, and libopus.

```sh
make client-cpp
target/rdev-client-cpp/rdev-client-cpp
```

Enter the Sunshine/Apollo host address. Defaults are HTTP port `47989` and HTTPS port `47984`. For first-time pairing, enter the displayed four-digit PIN in the host's Sunshine/Apollo web UI or stdin, then confirm in the client. The client stores its pairing identity and pins the host certificate per endpoint. Focus the video surface to send keyboard and mouse input; connected SDL2 gamepads send buttons, sticks, triggers, and rumble.

The GameStream protocol and media integration use the vendored Moonlight-Web and moonlight-common-c code under `third_party/moonlight-web`; consult their bundled GPL-3.0 license files before redistribution.
