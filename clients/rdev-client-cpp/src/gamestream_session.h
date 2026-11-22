#pragma once

#include <QObject>
#include <QImage>
#include <QString>
#include <QVector>

struct GameStreamApp
{
    int id = 0;
    QString name;
};

class GameStreamSession final : public QObject
{
    Q_OBJECT

public:
    explicit GameStreamSession(QObject* parent = nullptr);
    ~GameStreamSession() override;

    void connectToHost(const QString& host, quint16 httpPort = 47989, quint16 httpsPort = 47984);
    void confirmPairingPinEntered();
    void launchApp(int appId, int width = 1920, int height = 1080, int fps = 60,
                   int bitrateKbps = 20000);
    void stopStream();

    void sendKey(int virtualKey, bool down, char modifiers = 0);
    void sendMouseMove(short deltaX, short deltaY);
    void sendMouseButton(bool down, int button);
    void sendMouseScroll(short amount);
    void setControllerConnected(bool connected);
    void sendControllerState(int buttonFlags, unsigned char leftTrigger, unsigned char rightTrigger,
                             short leftStickX, short leftStickY, short rightStickX,
                             short rightStickY);

signals:
    void gamepadRumble(unsigned short lowFrequency, unsigned short highFrequency);

    void statusChanged(const QString& status);
    void pairingPinRequired(const QString& clientPin);
    void appsReady(const QVector<GameStreamApp>& apps);
    void videoFrameReady(const QImage& frame);
    void streamStopped();
    void errorOccurred(const QString& message);
    void streamStateChanged(bool active);
};
