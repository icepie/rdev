#include "gamestream_session.h"

#include "backend/IdentityManager.h"
#include "backend/NvHTTP.h"
#include "backend/NvPairingManager.h"
#include "Limelight.h"

#include <QCryptographicHash>
#include <QNetworkProxy>
#include <QRandomGenerator>
#include <QSet>
#include <QSettings>
#include <QSslCertificate>
#include <QThread>
#include <QXmlStreamReader>

#include <SDL.h>
#include <opus_multistream.h>
#include <openssl/rand.h>
extern "C" {
#include <libavcodec/avcodec.h>
#include <libavutil/error.h>
#include <libswscale/swscale.h>
}

#include <cerrno>
#include <atomic>
#include <condition_variable>
#include <cstring>
#include <functional>
#include <limits>
#include <memory>
#include <mutex>
#include <shared_mutex>
#include <thread>
#include <vector>

namespace {
class SessionState;

// moonlight-common-c has process-global state and context-free callbacks.
// Keep its owner registered until StopConnection has joined the media threads.
// The lock also fences the library's detached termination callback against deletion.
std::shared_mutex callbackMutex;
SessionState* callbackOwner = nullptr;

QString ffmpegError(int code)
{
    char text[AV_ERROR_MAX_STRING_SIZE] = {};
    av_strerror(code, text, sizeof(text));
    return QString::fromUtf8(text);
}

class SessionState final : public QObject
{
public:
    explicit SessionState(GameStreamSession* session)
        : QObject(session), owner(session), network(this), http(&network, this)
    {
        network.setProxy(QNetworkProxy::NoProxy);
    }

    ~SessionState() override
    {
        closing.store(true);
        cancelRequests();
        requestStop();
        if (worker.joinable()) worker.join();
        // Setup failures may occur before common-c installs its cleanup callbacks.
        cleanupVideo();
        cleanupAudio();
    }

    GameStreamSession* const owner;
    QNetworkAccessManager network;
    NvHTTP http;
    std::unique_ptr<NvPairingManager> pairing;
    QSet<QNetworkReply*> requests;
    quint64 requestGeneration = 0;
    NvAddress address;
    quint16 httpsPort = 0;
    QString certificateKey;
    ClientIdentity identity;
    QString uniqueId;
    QByteArray serverCertificate;
    QString appVersion;
    QString gfeVersion;
    int codecSupport = SCM_H264;
    int currentGame = 0;
    QString pendingPin;
    bool pairingPinConfirmed = false;
    bool pairingInitialized = false;
    bool pairingCompleting = false;
    bool paired = false;
    bool launching = false;

    std::atomic<bool> closing{false};
    std::atomic<bool> stopping{false};
    std::atomic<quint64> streamGeneration{0};
    std::thread worker;
    std::mutex stopMutex;
    std::condition_variable stopCondition;
    std::mutex inputMutex;
    bool inputReady = false;
    bool controllerConnected = false;
    std::mutex frameMutex;
    QImage latestFrame;
    bool frameQueued = false;

    AVCodecContext* decoder = nullptr;
    AVFrame* decodedFrame = nullptr;
    AVPacket* packet = nullptr;
    SwsContext* scaler = nullptr;
    OpusMSDecoder* opus = nullptr;
    SDL_AudioDeviceID audioDevice = 0;
    bool audioSubsystem = false;
    int audioChannels = 0;
    int audioFrameSamples = 0;
    Uint32 maximumQueuedAudio = 0;
    std::vector<opus_int16> samples;

    template<typename Function>
    void post(Function function)
    {
        const quint64 generation = streamGeneration.load();
        QMetaObject::invokeMethod(this, [this, generation, function = std::move(function)] {
            if (!closing.load() && generation == streamGeneration.load()) function();
        }, Qt::QueuedConnection);
    }

    void error(const QString& message)
    {
        const bool wasLaunching = launching;
        launching = false;
        emit owner->errorOccurred(message);
        if (wasLaunching) {
            emit owner->streamStateChanged(false);
            emit owner->streamStopped();
        }
    }

    void cancelRequests()
    {
        ++requestGeneration;
        const auto pending = std::move(requests);
        requests.clear();
        for (QNetworkReply* reply : pending) {
            disconnect(reply, nullptr, this, nullptr);
            reply->abort();
            reply->deleteLater();
        }
        // Invalidate callbacks before deleting the pairing manager's network replies.
        pairing.reset();
        pairingInitialized = false;
        pairingCompleting = false;
        pairingPinConfirmed = false;
        pendingPin.clear();
    }

    void watch(QNetworkReply* reply, std::function<void(const QString&)> success)
    {
        const quint64 generation = requestGeneration;
        requests.insert(reply);
        connect(reply, &QNetworkReply::finished, this,
                [this, reply, generation, success = std::move(success)] {
            requests.remove(reply);
            reply->deleteLater();
            if (closing.load() || generation != requestGeneration) return;
            if (NvHTTP::serverCertRefused(reply)) {
                error(tr("The host certificate does not match the paired host. The saved certificate was not changed."));
                return;
            }
            if (reply->error() != QNetworkReply::NoError) {
                error(reply->errorString());
                return;
            }
            const QString xml = QString::fromUtf8(reply->readAll());
            QXmlStreamReader reader(xml);
            while (!reader.atEnd()) reader.readNext();
            if (reader.hasError() || NvHTTP::responseStatusCode(xml) == 0) {
                error(tr("The host returned an invalid GameStream response."));
                return;
            }
            try {
                NvHTTP::verifyResponseStatus(xml);
                success(xml);
            } catch (const std::exception& exception) {
                error(QString::fromUtf8(exception.what()));
            }
        });
    }

    void readHostInfo(const QString& xml)
    {
        appVersion = NvHTTP::getXmlString(xml, QStringLiteral("appversion"));
        gfeVersion = NvHTTP::getXmlString(xml, QStringLiteral("GfeVersion"));
        const QString codecs = NvHTTP::getXmlString(xml, QStringLiteral("ServerCodecModeSupport"));
        codecSupport = codecs.isEmpty() ? SCM_H264 : codecs.toInt();
        currentGame = NvHTTP::getCurrentGame(xml);
    }

    void connectHost(const QString& host, quint16 httpPort, quint16 securePort)
    {
        if (worker.joinable()) {
            error(tr("Stop the current stream before connecting to another host."));
            return;
        }
        cancelRequests();
        launching = false;
        paired = false;
        if (host.trimmed().isEmpty() || httpPort == 0 || securePort == 0) {
            error(tr("Enter a host name and valid HTTP/HTTPS ports."));
            return;
        }
        address = NvAddress(host.trimmed(), httpPort);
        httpsPort = securePort;
        // Include both ports: multiple independent hosts can share one IP address.
        const QByteArray endpoint = address.address().toLower().toUtf8() + ':' +
            QByteArray::number(httpPort) + ':' + QByteArray::number(httpsPort);
        certificateKey = QStringLiteral("gamestream/hosts/%1/serverCertificate")
            .arg(QString::fromLatin1(QCryptographicHash::hash(endpoint, QCryptographicHash::Sha256).toHex()));
        serverCertificate = QSettings().value(certificateKey).toByteArray();
        try {
            IdentityManager* manager = IdentityManager::get();
            identity = {manager->getCertificate(), manager->getPrivateKey()};
            uniqueId = manager->getUniqueId();
            if (!identity.isValid() || uniqueId.isEmpty()) {
                error(tr("Unable to load the persistent client identity."));
                return;
            }
            emit owner->statusChanged(tr("Connecting to %1…").arg(address.address()));
            QNetworkReply* reply = serverCertificate.isEmpty()
                ? http.getServerInfoAsync(address, uniqueId)
                : http.getServerInfoAsyncHttps(address, uniqueId, identity.certPem,
                                              identity.keyPem, serverCertificate, httpsPort);
            watch(reply, [this](const QString& xml) {
                readHostInfo(xml);
                if (appVersion.isEmpty()) {
                    error(tr("The host did not provide a GameStream version."));
                    return;
                }
                if (!serverCertificate.isEmpty() &&
                    NvHTTP::getXmlString(xml, QStringLiteral("PairStatus")) == QStringLiteral("1")) {
                    fetchApps(false);
                } else {
                    beginPairing();
                }
            });
        } catch (const std::exception& exception) {
            error(QString::fromUtf8(exception.what()));
        }
    }

    void beginPairing()
    {
        pendingPin = QStringLiteral("%1").arg(QRandomGenerator::system()->bounded(10000), 4, 10, QLatin1Char('0'));
        pairingPinConfirmed = false;
        pairing = std::make_unique<NvPairingManager>(appVersion, address.address(),
                                                    address.port(), httpsPort, identity, uniqueId);
        const quint64 generation = requestGeneration;
        pairing->initiatePairing([this, generation](NvPairingManager::InitResult result) {
            if (closing.load() || generation != requestGeneration) return;
            if (result != NvPairingManager::INIT_OK) {
                error(result == NvPairingManager::INIT_ALREADY_IN_PROGRESS
                          ? tr("Another pairing is already in progress on the host.")
                          : tr("Pairing could not start. Check the host and reconnect."));
                return;
            }
            pairingInitialized = true;
            completePairing();
        });
        emit owner->pairingPinRequired(pendingPin);
    }

    void confirmPinEntered()
    {
        if (!pairing || pairingCompleting || paired || pendingPin.isEmpty()) return;
        pairingPinConfirmed = true;
        emit owner->statusChanged(tr("Pairing with the host…"));
        completePairing();
    }

    void completePairing()
    {
        if (!pairingInitialized || !pairingPinConfirmed || pendingPin.isEmpty() || pairingCompleting) return;
        pairingCompleting = true;
        const quint64 generation = requestGeneration;
        pairing->completePairing(pendingPin, [this, generation](NvPairingManager::PairState result,
                                                              const QByteArray& certificate) {
            if (closing.load() || generation != requestGeneration) return;
            pairingCompleting = false;
            pendingPin.clear();
            if (result != NvPairingManager::PAIRED) {
                error(result == NvPairingManager::PIN_WRONG
                          ? tr("The pairing PIN was rejected. Reconnect to start a new pairing.")
                          : tr("Pairing failed. Reconnect to try again."));
                return;
            }
            const QSslCertificate presented(certificate, QSsl::Pem);
            if (presented.isNull() || (!serverCertificate.isEmpty() &&
                presented != QSslCertificate(serverCertificate, QSsl::Pem))) {
                error(tr("Pairing returned an invalid or changed host certificate."));
                return;
            }
            serverCertificate = certificate;
            // NvPairingManager can report success after a lost response. Prove
            // mutual TLS and the pin before persisting it or enabling launch.
            fetchApps(true);
        });
    }

    void fetchApps(bool persistCertificate)
    {
        watch(http.getAppListAsync(address, httpsPort, identity.certPem, identity.keyPem,
                                   serverCertificate), [this, persistCertificate](const QString& xml) {
            const auto hostApps = NvHTTP::parseAppList(xml);
            if (persistCertificate) {
                QSettings settings;
                settings.setValue(certificateKey, serverCertificate);
                settings.sync();
                if (settings.status() != QSettings::NoError) {
                    error(tr("Unable to persist the paired host certificate."));
                    return;
                }
            }
            paired = true;
            QVector<GameStreamApp> apps;
            apps.reserve(hostApps.size());
            for (const NvApp& app : hostApps) apps.append({app.id(), app.name()});
            emit owner->statusChanged(tr("Connected. Select an application to stream."));
            emit owner->appsReady(apps);
        });
    }

    void launch(int appId, int width, int height, int fps, int bitrate)
    {
        if (launching || worker.joinable()) return;
        if (!paired || appId <= 0 || width <= 0 || height <= 0 || fps <= 0 ||
            fps > std::numeric_limits<int>::max() / 100 || bitrate <= 0) {
            error(tr("Connect to a paired host and select valid stream settings first."));
            return;
        }
        launching = true;
        emit owner->statusChanged(tr("Launching application…"));
        watch(http.getServerInfoAsyncHttps(address, uniqueId, identity.certPem, identity.keyPem,
                                           serverCertificate, httpsPort),
              [this, appId, width, height, fps, bitrate](const QString& xml) {
            readHostInfo(xml);
            QByteArray key(16, '\0');
            unsigned char randomId[4];
            if (RAND_bytes(reinterpret_cast<unsigned char*>(key.data()), key.size()) != 1 ||
                RAND_bytes(randomId, sizeof(randomId)) != 1) {
                error(tr("Unable to generate stream encryption keys."));
                return;
            }
            const int keyId = int(((quint32(randomId[0]) << 24) | (quint32(randomId[1]) << 16) |
                                   (quint32(randomId[2]) << 8) | randomId[3]) & 0x7fffffffU);
            QNetworkReply* reply = currentGame == appId
                ? http.resumeAppAsync(address, httpsPort, uniqueId, key, keyId,
                                      identity.certPem, identity.keyPem, serverCertificate)
                : http.launchAppAsync(address, httpsPort, appId, uniqueId, key, keyId,
                                      width, height, fps, bitrate, identity.certPem,
                                      identity.keyPem, serverCertificate);
            watch(reply, [this, width, height, fps, bitrate, key, keyId](const QString& response) {
                const QString sessionUrl = NvHTTP::parseSessionUrl(response);
                if (sessionUrl.isEmpty()) {
                    error(tr("The launch response did not contain a streaming session URL."));
                    return;
                }
                startStream(sessionUrl, width, height, fps, bitrate, key, keyId);
            });
        });
    }

    void requestStop()
    {
        {
            std::lock_guard<std::mutex> lock(stopMutex);
            stopping.store(true);
        }
        {
            std::lock_guard<std::mutex> lock(inputMutex);
            inputReady = false;
        }
        {
            std::shared_lock<std::shared_mutex> lock(callbackMutex);
            if (callbackOwner == this) LiInterruptConnection();
        }
        stopCondition.notify_all();
    }

    void stop()
    {
        const bool hadLaunch = launching;
        launching = false;
        cancelRequests();
        requestStop();
        if (!worker.joinable()) {
            if (hadLaunch) emit owner->statusChanged(tr("Launch canceled."));
            emit owner->gamepadRumble(0, 0);
            emit owner->streamStateChanged(false);
            emit owner->streamStopped();
        } else {
            emit owner->statusChanged(tr("Stopping stream…"));
        }
    }

    template<typename Function>
    static auto callback(Function function) -> decltype(function(static_cast<SessionState*>(nullptr)))
    {
        std::shared_lock<std::shared_mutex> lock(callbackMutex);
        return function(callbackOwner);
    }

    void startStream(const QString& rtspUrl, int width, int height, int fps, int bitrate,
                     const QByteArray& key, int keyId)
    {
        {
            std::unique_lock<std::shared_mutex> lock(callbackMutex);
            if (callbackOwner) {
                lock.unlock();
                error(tr("Another native stream is already active."));
                return;
            }
            callbackOwner = this;
        }
        stopping.store(false);
        ++streamGeneration;
        const QByteArray hostBytes = address.address().toUtf8();
        const QByteArray versionBytes = appVersion.toUtf8();
        const QByteArray gfeBytes = gfeVersion.toUtf8();
        const QByteArray urlBytes = rtspUrl.toUtf8();
        const int hostCodecs = codecSupport;
        try {
            worker = std::thread([this, hostBytes, versionBytes, gfeBytes, urlBytes, hostCodecs,
                                  width, height, fps, bitrate, key, keyId] {
                SERVER_INFORMATION server;
                LiInitializeServerInformation(&server);
                server.address = hostBytes.constData();
                server.serverInfoAppVersion = versionBytes.constData();
                server.serverInfoGfeVersion = gfeBytes.isEmpty() ? nullptr : gfeBytes.constData();
                server.rtspSessionUrl = urlBytes.isEmpty() ? nullptr : urlBytes.constData();
                server.serverCodecModeSupport = hostCodecs;
                STREAM_CONFIGURATION config;
                LiInitializeStreamConfiguration(&config);
                config.width = width;
                config.height = height;
                config.fps = fps;
                config.bitrate = bitrate;
                config.packetSize = 1024;
                config.streamingRemotely = STREAM_CFG_AUTO;
                config.audioConfiguration = AUDIO_CONFIGURATION_STEREO;
                if (avcodec_find_decoder(AV_CODEC_ID_H264)) config.supportedVideoFormats |= VIDEO_FORMAT_H264;
                if (avcodec_find_decoder(AV_CODEC_ID_HEVC)) config.supportedVideoFormats |= VIDEO_FORMAT_H265;
                config.clientRefreshRateX100 = fps * 100;
                config.colorSpace = COLORSPACE_REC_709;
                config.colorRange = COLOR_RANGE_LIMITED;
                config.encryptionFlags = ENCFLG_ALL;
                std::memcpy(config.remoteInputAesKey, key.constData(), sizeof(config.remoteInputAesKey));
                const quint32 iv = quint32(keyId);
                for (int i = 0; i < 4; ++i) config.remoteInputAesIv[i] = char(iv >> (24 - i * 8));

                DECODER_RENDERER_CALLBACKS video;
                LiInitializeVideoCallbacks(&video);
                video.setup = [](int format, int w, int h, int, void* context, int) {
                    return static_cast<SessionState*>(context)->setupVideo(format, w, h);
                };
                video.cleanup = [] { callback([](SessionState* state) { if (state) state->cleanupVideo(); }); };
                video.submitDecodeUnit = [](PDECODE_UNIT unit) {
                    return callback([unit](SessionState* state) {
                        return state && !state->stopping.load() ? state->decodeVideo(unit) : DR_OK;
                    });
                };
                // Software decoding runs on common-c's decoder thread, not its UDP receiver.
                AUDIO_RENDERER_CALLBACKS audio;
                LiInitializeAudioCallbacks(&audio);
                audio.init = [](int, const POPUS_MULTISTREAM_CONFIGURATION opusConfig, void* context, int) {
                    auto* state = static_cast<SessionState*>(context);
                    try {
                        return state->setupAudio(opusConfig);
                    } catch (const std::bad_alloc&) {
                        state->cleanupAudio();
                        return -1;
                    }
                };
                audio.start = [] { callback([](SessionState* state) {
                    if (state && state->audioDevice) SDL_PauseAudioDevice(state->audioDevice, 0);
                }); };
                audio.cleanup = [] { callback([](SessionState* state) { if (state) state->cleanupAudio(); }); };
                audio.decodeAndPlaySample = [](char* data, int length) {
                    callback([data, length](SessionState* state) {
                        if (state && !state->stopping.load()) state->decodeAudio(data, length);
                    });
                };
                audio.capabilities = CAPABILITY_SUPPORTS_ARBITRARY_AUDIO_DURATION;
                CONNECTION_LISTENER_CALLBACKS listener;
                LiInitializeConnectionCallbacks(&listener);
                listener.stageStarting = [](int stage) { callback([stage](SessionState* state) {
                    if (!state) return;
                    // StartConnection resets its interrupt flag. Reassert cancellation
                    // from the first stage callback if Stop raced that reset.
                    if (state->stopping.load()) LiInterruptConnection();
                    const QString name = QString::fromUtf8(LiGetStageName(stage));
                    state->post([state, name] {
                        if (!state->stopping.load()) emit state->owner->statusChanged(name);
                    });
                }); };
                listener.connectionTerminated = [](int code) { callback([code](SessionState* state) {
                    if (!state) return;
                    {
                        std::lock_guard<std::mutex> lock(state->stopMutex);
                        if (state->stopping.exchange(true)) return;
                    }
                    if (code != ML_ERROR_GRACEFUL_TERMINATION) state->post([state, code] {
                        emit state->owner->errorOccurred(tr("Stream terminated (error %1).").arg(code));
                    });
                    // Never stop common-c from one of its callbacks.
                    state->stopCondition.notify_all();
                }); };
                listener.rumble = [](unsigned short controller, unsigned short low, unsigned short high) {
                    callback([controller, low, high](SessionState* state) {
                        if (state && controller == 0) state->post([state, low, high] {
                            bool connected;
                            { std::lock_guard<std::mutex> lock(state->inputMutex);
                              connected = state->inputReady && state->controllerConnected; }
                            if (connected) emit state->owner->gamepadRumble(low, high);
                        });
                    });
                };
                const int result = stopping.load() ? -1 :
                    LiStartConnection(&server, &config, &listener, &video, &audio, this, 0, this, 0);
                if (result == 0) {
                    {
                        std::lock_guard<std::mutex> lock(inputMutex);
                        inputReady = !stopping.load();
                        if (inputReady && controllerConnected) announceController();
                    }
                    post([this] {
                        launching = false;
                        if (!stopping.load()) {
                            emit owner->statusChanged(tr("Streaming"));
                            emit owner->streamStateChanged(true);
                        }
                    });
                    {
                        std::unique_lock<std::mutex> lock(stopMutex);
                        stopCondition.wait(lock, [this] { return stopping.load(); });
                    }
                    { std::lock_guard<std::mutex> lock(inputMutex); inputReady = false; }
                    LiStopConnection();
                } else if (!stopping.load()) {
                    post([this, result] { emit owner->errorOccurred(tr("Unable to start stream (error %1).").arg(result)); });
                }
                cleanupVideo();
                cleanupAudio();
                {
                    std::unique_lock<std::shared_mutex> lock(callbackMutex);
                    callbackOwner = nullptr;
                }
                post([this] {
                    if (worker.joinable()) worker.join();
                    launching = false;
                    { std::lock_guard<std::mutex> lock(frameMutex); latestFrame = {}; }
                    emit owner->gamepadRumble(0, 0);
                    emit owner->streamStateChanged(false);
                    emit owner->streamStopped();
                });
            });
        } catch (const std::exception& exception) {
            { std::unique_lock<std::shared_mutex> lock(callbackMutex); callbackOwner = nullptr; }
            error(QString::fromUtf8(exception.what()));
        }
    }

    int setupVideo(int format, int width, int height)
    {
        const AVCodecID codecId = (format & VIDEO_FORMAT_MASK_H264) ? AV_CODEC_ID_H264 :
            (format & VIDEO_FORMAT_MASK_H265) ? AV_CODEC_ID_HEVC : AV_CODEC_ID_NONE;
        const AVCodec* codec = avcodec_find_decoder(codecId);
        if (!codec) return -1;
        decoder = avcodec_alloc_context3(codec);
        decodedFrame = av_frame_alloc();
        packet = av_packet_alloc();
        if (!decoder || !decodedFrame || !packet) { cleanupVideo(); return -1; }
        decoder->width = width;
        decoder->height = height;
        decoder->flags |= AV_CODEC_FLAG_LOW_DELAY;
        decoder->thread_type = FF_THREAD_SLICE;
        decoder->thread_count = qBound(1, QThread::idealThreadCount(), 8);
        const int result = avcodec_open2(decoder, codec, nullptr);
        if (result < 0) {
            const QString message = tr("Video decoder: %1").arg(ffmpegError(result));
            post([this, message] { emit owner->errorOccurred(message); });
            cleanupVideo();
        }
        return result;
    }

    void cleanupVideo()
    {
        sws_freeContext(scaler);
        scaler = nullptr;
        av_packet_free(&packet);
        av_frame_free(&decodedFrame);
        avcodec_free_context(&decoder);
    }

    int receiveFrames()
    {
        int result;
        while ((result = avcodec_receive_frame(decoder, decodedFrame)) == 0) {
            const int width = decodedFrame->width;
            const int height = decodedFrame->height;
            scaler = sws_getCachedContext(scaler, width, height, AVPixelFormat(decodedFrame->format),
                                         width, height, AV_PIX_FMT_RGBA, SWS_BILINEAR, nullptr, nullptr, nullptr);
            if (!scaler) { av_frame_unref(decodedFrame); return AVERROR(ENOMEM); }
            int colorspace = SWS_CS_ITU709;
            if (decodedFrame->colorspace == AVCOL_SPC_BT470BG || decodedFrame->colorspace == AVCOL_SPC_SMPTE170M)
                colorspace = SWS_CS_ITU601;
            else if (decodedFrame->colorspace == AVCOL_SPC_BT2020_NCL || decodedFrame->colorspace == AVCOL_SPC_BT2020_CL)
                colorspace = SWS_CS_BT2020;
            const int* coefficients = sws_getCoefficients(colorspace);
            sws_setColorspaceDetails(scaler, coefficients, decodedFrame->color_range == AVCOL_RANGE_JPEG,
                                    coefficients, 1, 0, 1 << 16, 1 << 16);
            QImage image(width, height, QImage::Format_RGBA8888);
            if (image.isNull()) { av_frame_unref(decodedFrame); return AVERROR(ENOMEM); }
            uint8_t* destination[] = {image.bits(), nullptr, nullptr, nullptr};
            const int strides[] = {int(image.bytesPerLine()), 0, 0, 0};
            const int rows = sws_scale(scaler, decodedFrame->data, decodedFrame->linesize, 0, height,
                                       destination, strides);
            av_frame_unref(decodedFrame);
            if (rows != height) return AVERROR_INVALIDDATA;
            bool enqueue = false;
            {
                std::lock_guard<std::mutex> lock(frameMutex);
                latestFrame = std::move(image);
                if (!frameQueued) { frameQueued = true; enqueue = true; }
            }
            if (enqueue) post([this] {
                QImage image;
                {
                    std::lock_guard<std::mutex> lock(frameMutex);
                    image = std::move(latestFrame);
                    frameQueued = false;
                }
                if (!stopping.load() && !image.isNull()) emit owner->videoFrameReady(image);
            });
        }
        return result == AVERROR(EAGAIN) || result == AVERROR_EOF ? 0 : result;
    }

    int decodeVideo(PDECODE_UNIT unit)
    {
        if (!decoder || unit->fullLength <= 0 ||
            unit->fullLength > std::numeric_limits<int>::max() - AV_INPUT_BUFFER_PADDING_SIZE) return DR_NEED_IDR;
        const int size = unit->fullLength;
        if (!packet->buf || packet->buf->size < size_t(size + AV_INPUT_BUFFER_PADDING_SIZE)) {
            av_packet_unref(packet);
            if (av_new_packet(packet, size) < 0) return DR_NEED_IDR;
        } else if (av_buffer_make_writable(&packet->buf) < 0) {
            return DR_NEED_IDR;
        }
        packet->data = packet->buf->data;
        packet->size = size;
        packet->pts = int64_t(unit->presentationTimeUs);
        packet->flags = unit->frameType == FRAME_TYPE_IDR ? AV_PKT_FLAG_KEY : 0;
        int offset = 0;
        for (PLENTRY entry = unit->bufferList; entry; entry = entry->next) {
            if (entry->length <= 0 || entry->length > size - offset) return DR_NEED_IDR;
            std::memcpy(packet->data + offset, entry->data, size_t(entry->length));
            offset += entry->length;
        }
        if (offset != size) return DR_NEED_IDR;
        std::memset(packet->data + size, 0, AV_INPUT_BUFFER_PADDING_SIZE);
        int result = avcodec_send_packet(decoder, packet);
        if (result == AVERROR(EAGAIN)) {
            result = receiveFrames();
            if (result >= 0) result = avcodec_send_packet(decoder, packet);
        }
        if (result >= 0) result = receiveFrames();
        if (result < 0) {
            avcodec_flush_buffers(decoder);
            return DR_NEED_IDR;
        }
        return DR_OK;
    }

    int setupAudio(const OPUS_MULTISTREAM_CONFIGURATION* config)
    {
        int result = OPUS_OK;
        opus = opus_multistream_decoder_create(config->sampleRate, config->channelCount, config->streams,
                                               config->coupledStreams, config->mapping, &result);
        if (!opus || result != OPUS_OK) { cleanupAudio(); return -1; }
        if (SDL_InitSubSystem(SDL_INIT_AUDIO) != 0) {
            const QString message = tr("Audio initialization: %1").arg(QString::fromUtf8(SDL_GetError()));
            post([this, message] { emit owner->errorOccurred(message); });
            cleanupAudio();
            return -1;
        }
        audioSubsystem = true;
        audioChannels = config->channelCount;
        audioFrameSamples = config->samplesPerFrame;
        // Opus packets may contain up to 120 ms; retain one reusable PCM buffer.
        samples.resize(size_t(config->sampleRate * 120 / 1000) * size_t(audioChannels));
        SDL_AudioSpec desired{};
        desired.freq = config->sampleRate;
        desired.format = AUDIO_S16SYS;
        desired.channels = Uint8(audioChannels);
        desired.samples = 512;
        audioDevice = SDL_OpenAudioDevice(nullptr, 0, &desired, nullptr, 0);
        if (!audioDevice) {
            const QString message = tr("Audio output: %1").arg(QString::fromUtf8(SDL_GetError()));
            post([this, message] { emit owner->errorOccurred(message); });
            cleanupAudio();
            return -1;
        }
        maximumQueuedAudio = Uint32(config->sampleRate * audioChannels * int(sizeof(opus_int16)) / 10);
        return 0;
    }

    void cleanupAudio()
    {
        if (audioDevice) SDL_CloseAudioDevice(audioDevice);
        audioDevice = 0;
        if (opus) opus_multistream_decoder_destroy(opus);
        opus = nullptr;
        if (audioSubsystem) SDL_QuitSubSystem(SDL_INIT_AUDIO);
        audioSubsystem = false;
        samples.clear();
    }

    void decodeAudio(const char* data, int length)
    {
        if (!opus || !audioDevice) return;
        const int capacity = data ? int(samples.size() / size_t(audioChannels)) : audioFrameSamples;
        const int count = opus_multistream_decode(opus, reinterpret_cast<const unsigned char*>(data),
                                                  length, samples.data(), capacity, 0);
        if (count < 0) return;
        // Do not accumulate latency if the playback device or desktop stalls.
        if (SDL_GetQueuedAudioSize(audioDevice) > maximumQueuedAudio) SDL_ClearQueuedAudio(audioDevice);
        if (SDL_QueueAudio(audioDevice, samples.data(), Uint32(count * audioChannels * int(sizeof(opus_int16)))) != 0) {
            const QString message = tr("Audio playback: %1").arg(QString::fromUtf8(SDL_GetError()));
            post([this, message] { emit owner->errorOccurred(message); });
            { std::lock_guard<std::mutex> lock(stopMutex); stopping.store(true); }
            stopCondition.notify_all();
        }
    }

    void announceController()
    {
        LiSendControllerArrivalEvent(0, 1, LI_CTYPE_XBOX, 0xF7FF,
                                     LI_CCAP_ANALOG_TRIGGERS | LI_CCAP_RUMBLE);
    }
};

SessionState* stateFor(GameStreamSession* session)
{
    // Keep the public QObject interface unchanged; the private implementation is
    // its child and is destroyed explicitly before GameStreamSession's signals.
    for (QObject* child : session->children()) {
        if (auto* state = dynamic_cast<SessionState*>(child)) return state;
    }
    Q_UNREACHABLE();
}
} // namespace

GameStreamSession::GameStreamSession(QObject* parent) : QObject(parent)
{
    new SessionState(this);
}

GameStreamSession::~GameStreamSession()
{
    delete stateFor(this);
}

void GameStreamSession::connectToHost(const QString& host, quint16 httpPort, quint16 httpsPort)
{
    stateFor(this)->connectHost(host, httpPort, httpsPort);
}

void GameStreamSession::confirmPairingPinEntered()
{
    stateFor(this)->confirmPinEntered();
}

void GameStreamSession::launchApp(int appId, int width, int height, int fps, int bitrateKbps)
{
    stateFor(this)->launch(appId, width, height, fps, bitrateKbps);
}

void GameStreamSession::stopStream()
{
    stateFor(this)->stop();
}

void GameStreamSession::sendKey(int virtualKey, bool down, char modifiers)
{
    auto* state = stateFor(this);
    std::lock_guard<std::mutex> lock(state->inputMutex);
    if (state->inputReady && virtualKey > 0 && virtualKey <= 0xff)
        LiSendKeyboardEvent(short(virtualKey | 0x8000), down ? KEY_ACTION_DOWN : KEY_ACTION_UP, modifiers);
}

void GameStreamSession::sendMouseMove(short deltaX, short deltaY)
{
    auto* state = stateFor(this);
    std::lock_guard<std::mutex> lock(state->inputMutex);
    if (state->inputReady) LiSendMouseMoveEvent(deltaX, deltaY);
}

void GameStreamSession::sendMouseButton(bool down, int button)
{
    auto* state = stateFor(this);
    std::lock_guard<std::mutex> lock(state->inputMutex);
    if (state->inputReady && button >= BUTTON_LEFT && button <= BUTTON_X2)
        LiSendMouseButtonEvent(down ? BUTTON_ACTION_PRESS : BUTTON_ACTION_RELEASE, button);
}

void GameStreamSession::sendMouseScroll(short amount)
{
    auto* state = stateFor(this);
    std::lock_guard<std::mutex> lock(state->inputMutex);
    if (state->inputReady) LiSendHighResScrollEvent(amount);
}

void GameStreamSession::setControllerConnected(bool connected)
{
    auto* state = stateFor(this);
    {
        std::lock_guard<std::mutex> lock(state->inputMutex);
        if (state->controllerConnected == connected) return;
        state->controllerConnected = connected;
        if (state->inputReady) {
            if (connected) state->announceController();
            else LiSendMultiControllerEvent(0, 0, 0, 0, 0, 0, 0, 0, 0);
        }
    }
    if (!connected) emit gamepadRumble(0, 0);
}

void GameStreamSession::sendControllerState(int buttonFlags, unsigned char leftTrigger,
                                          unsigned char rightTrigger, short leftStickX,
                                          short leftStickY, short rightStickX, short rightStickY)
{
    auto* state = stateFor(this);
    std::lock_guard<std::mutex> lock(state->inputMutex);
    if (state->inputReady && state->controllerConnected)
        LiSendMultiControllerEvent(0, 1, buttonFlags, leftTrigger, rightTrigger,
                                   leftStickX, leftStickY, rightStickX, rightStickY);
}
