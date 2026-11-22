#include "viewer_window.h"

#include <QComboBox>
#include <QCursor>
#include <QEvent>
#include <QFormLayout>
#include <QHBoxLayout>
#include <QKeyEvent>
#include <QLabel>
#include <QLineEdit>
#include <QMouseEvent>
#include <QPushButton>
#include <QSettings>
#include <QSpinBox>
#include <QVBoxLayout>
#include <QWheelEvent>
#include <QPixmap>
#include <QWidget>
#include <QTimer>

#include <SDL.h>

#include <utility>

namespace {
constexpr int kModShift = 0x01;
constexpr int kModCtrl = 0x02;
constexpr int kModAlt = 0x04;
constexpr int kModMeta = 0x08;

int virtualKey(int key)
{
    if ((key >= Qt::Key_A && key <= Qt::Key_Z) || (key >= Qt::Key_0 && key <= Qt::Key_9))
        return key;
    if (key >= Qt::Key_F1 && key <= Qt::Key_F24) return 0x70 + key - Qt::Key_F1;
    switch (key) {
    case Qt::Key_Backspace: return 0x08;
    case Qt::Key_Tab: return 0x09;
    case Qt::Key_Return: case Qt::Key_Enter: return 0x0D;
    case Qt::Key_Shift: return 0x10;
    case Qt::Key_Control: return 0x11;
    case Qt::Key_Alt: return 0x12;
    case Qt::Key_Escape: return 0x1B;
    case Qt::Key_Space: return 0x20;
    case Qt::Key_PageUp: return 0x21;
    case Qt::Key_PageDown: return 0x22;
    case Qt::Key_End: return 0x23;
    case Qt::Key_Home: return 0x24;
    case Qt::Key_Left: return 0x25;
    case Qt::Key_Up: return 0x26;
    case Qt::Key_Right: return 0x27;
    case Qt::Key_Down: return 0x28;
    case Qt::Key_Insert: return 0x2D;
    case Qt::Key_Delete: return 0x2E;
    case Qt::Key_Meta: return 0x5B;
    default: return 0;
    }
}

char keyModifiers(Qt::KeyboardModifiers modifiers)
{
    char result = 0;
    if (modifiers.testFlag(Qt::ShiftModifier)) result |= kModShift;
    if (modifiers.testFlag(Qt::ControlModifier)) result |= kModCtrl;
    if (modifiers.testFlag(Qt::AltModifier)) result |= kModAlt;
    if (modifiers.testFlag(Qt::MetaModifier)) result |= kModMeta;
    return result;
}

int mouseButton(Qt::MouseButton button)
{
    switch (button) {
    case Qt::LeftButton: return 1;
    case Qt::RightButton: return 3;
    case Qt::MiddleButton: return 2;
    case Qt::XButton1: return 4;
    case Qt::XButton2: return 5;
    default: return 0;
    }
}
}

ViewerWindow::ViewerWindow(QWidget* parent) : QMainWindow(parent)
{
    setWindowTitle(tr("RDev GameStream"));
    resize(1200, 800);

    auto* central = new QWidget(this);
    auto* root = new QVBoxLayout(central);
    auto* hostRow = new QHBoxLayout;
    m_Host = new QLineEdit;
    m_Host->setPlaceholderText(tr("Sunshine / Apollo host address"));
    m_HttpPort = new QSpinBox;
    m_HttpPort->setRange(1, 65535);
    m_HttpPort->setValue(47989);
    m_HttpsPort = new QSpinBox;
    m_HttpsPort->setRange(1, 65535);
    m_HttpsPort->setValue(47984);
    m_Connect = new QPushButton(tr("Connect"));
    hostRow->addWidget(m_Host, 1);
    hostRow->addWidget(new QLabel(tr("HTTP")));
    hostRow->addWidget(m_HttpPort);
    hostRow->addWidget(new QLabel(tr("HTTPS")));
    hostRow->addWidget(m_HttpsPort);
    hostRow->addWidget(m_Connect);
    root->addLayout(hostRow);

    auto* pairRow = new QHBoxLayout;
    m_Pin = new QLineEdit;
    m_Pin->setPlaceholderText(tr("PIN to enter in Sunshine / Apollo Web UI"));
    m_Pin->setMaxLength(4);
    m_Pin->setReadOnly(true);
    m_Pair = new QPushButton(tr("I entered the PIN"));
    m_Pair->setEnabled(false);
    pairRow->addWidget(m_Pin, 1);
    pairRow->addWidget(m_Pair);
    m_Apps = new QComboBox;
    m_Launch = new QPushButton(tr("Launch"));
    m_Launch->setEnabled(false);
    m_Stop = new QPushButton(tr("Stop"));
    m_Stop->setEnabled(false);
    pairRow->addWidget(m_Apps, 1);
    pairRow->addWidget(m_Launch);
    pairRow->addWidget(m_Stop);
    root->addLayout(pairRow);

    m_Status = new QLabel(tr("Enter the host address to connect."));
    root->addWidget(m_Status);
    m_Video = new QLabel;
    m_Video->setAlignment(Qt::AlignCenter);
    m_Video->setMinimumSize(640, 360);
    m_Video->setStyleSheet(QStringLiteral("background: #111; color: #ddd"));
    m_Video->setText(tr("Not streaming"));
    m_Video->setFocusPolicy(Qt::StrongFocus);
    m_Video->setMouseTracking(true);
    m_Video->installEventFilter(this);
    root->addWidget(m_Video, 1);
    setCentralWidget(central);

    auto settings = QSettings{};
    m_Host->setText(settings.value(QStringLiteral("viewer/host")).toString());
    m_HttpPort->setValue(settings.value(QStringLiteral("viewer/httpPort"), 47989).toInt());
    m_HttpsPort->setValue(settings.value(QStringLiteral("viewer/httpsPort"), 47984).toInt());

    connect(m_Connect, &QPushButton::clicked, this, [this] {
        const QString host = m_Host->text().trimmed();
        if (host.isEmpty()) {
            m_Status->setText(tr("Host address is required."));
            return;
        }
        QSettings settings;
        settings.setValue(QStringLiteral("viewer/host"), host);
        settings.setValue(QStringLiteral("viewer/httpPort"), m_HttpPort->value());
        settings.setValue(QStringLiteral("viewer/httpsPort"), m_HttpsPort->value());
        m_Connect->setEnabled(false);
        m_Pair->setEnabled(false);
        m_Apps->clear();
        m_Session.connectToHost(host, quint16(m_HttpPort->value()), quint16(m_HttpsPort->value()));
    });
    connect(m_Pair, &QPushButton::clicked, this, [this] {
        m_Pair->setEnabled(false);
        m_Session.confirmPairingPinEntered();
    });
    connect(m_Launch, &QPushButton::clicked, this, [this] {
        const int id = m_Apps->currentData().toInt();
        if (id > 0) m_Session.launchApp(id);
    });
    connect(m_Stop, &QPushButton::clicked, &m_Session, &GameStreamSession::stopStream);
    connect(&m_Session, &GameStreamSession::statusChanged, m_Status, &QLabel::setText);
    connect(&m_Session, &GameStreamSession::pairingPinRequired, this, [this](const QString& pin) {
        m_Pin->setText(pin);
        m_Status->setText(tr("Enter this PIN in the host's Sunshine / Apollo web UI, then confirm."));
        m_Pair->setEnabled(true);
    });
    connect(&m_Session, &GameStreamSession::appsReady, this, [this](const QVector<GameStreamApp>& apps) {
        m_Apps->clear();
        for (const auto& app : apps) m_Apps->addItem(app.name, app.id);
        m_Launch->setEnabled(!apps.isEmpty());
        m_Connect->setEnabled(true);
    });
    connect(&m_Session, &GameStreamSession::videoFrameReady, this, [this](const QImage& frame) {
        if (!frame.isNull()) m_Video->setPixmap(QPixmap::fromImage(frame).scaled(
            m_Video->size(), Qt::KeepAspectRatio, Qt::SmoothTransformation));
    });
    connect(&m_Session, &GameStreamSession::gamepadRumble, this,
            [this](unsigned short low, unsigned short high) {
        if (m_Gamepad && SDL_GameControllerGetAttached(static_cast<SDL_GameController*>(m_Gamepad)))
            SDL_GameControllerRumble(static_cast<SDL_GameController*>(m_Gamepad), low, high, 500);
    });
    connect(&m_Session, &GameStreamSession::streamStateChanged, this, &ViewerWindow::setStreaming);
    connect(&m_Session, &GameStreamSession::errorOccurred, this, [this](const QString& message) {
        m_Status->setText(message);
        m_Connect->setEnabled(true);
        m_Pair->setEnabled(false);
    });
    connect(&m_Session, &GameStreamSession::streamStopped, this, [this] {
        setStreaming(false);
        m_Video->setPixmap({});
        m_Video->setText(tr("Not streaming"));
    });

    m_GamepadTimer = new QTimer(this);
    m_GamepadTimer->setInterval(16);
    connect(m_GamepadTimer, &QTimer::timeout, this, &ViewerWindow::refreshGamepad);
    m_GamepadTimer->start();
    SDL_InitSubSystem(SDL_INIT_GAMECONTROLLER | SDL_INIT_JOYSTICK);
}

ViewerWindow::~ViewerWindow()
{
    m_GamepadTimer->stop();
    releaseKeys();
    if (m_Gamepad) SDL_GameControllerClose(static_cast<SDL_GameController*>(m_Gamepad));
    if (m_GamepadConnected) m_Session.setControllerConnected(false);
    SDL_QuitSubSystem(SDL_INIT_GAMECONTROLLER | SDL_INIT_JOYSTICK);
    m_Session.stopStream();
}

void ViewerWindow::setStreaming(bool active)
{
    m_Streaming = active;
    if (!active && m_GamepadConnected) {
        m_GamepadConnected = false;
        m_Session.setControllerConnected(false);
    }
    m_Stop->setEnabled(active);
    m_Connect->setEnabled(!active);
    m_Launch->setEnabled(!active && m_Apps->count() > 0);
    if (active) {
        m_Video->setFocus();
        m_HaveMousePos = false;
    } else {
        releaseKeys();
        m_HaveMousePos = false;
    }
}

void ViewerWindow::releaseKeys()
{
    for (int key : std::as_const(m_PressedKeys)) m_Session.sendKey(key, false);
    m_PressedKeys.clear();
}

bool ViewerWindow::eventFilter(QObject* watched, QEvent* event)
{
    if (watched != m_Video || !m_Streaming) return QMainWindow::eventFilter(watched, event);
    if (event->type() == QEvent::KeyPress || event->type() == QEvent::KeyRelease) {
        auto* key = static_cast<QKeyEvent*>(event);
        const int vk = virtualKey(key->key());
        if (vk) {
            const bool down = event->type() == QEvent::KeyPress;
            if (down && !key->isAutoRepeat()) m_PressedKeys.insert(vk);
            if (!down && !key->isAutoRepeat()) m_PressedKeys.remove(vk);
            m_Session.sendKey(vk, down, keyModifiers(key->modifiers()));
            return true;
        }
    } else if (event->type() == QEvent::MouseMove) {
        auto* mouse = static_cast<QMouseEvent*>(event);
        const QPoint pos = mouse->position().toPoint();
        if (m_HaveMousePos) {
            const QPoint delta = pos - m_LastMousePos;
            if (!delta.isNull()) m_Session.sendMouseMove(short(delta.x()), short(delta.y()));
        }
        m_LastMousePos = pos;
        m_HaveMousePos = true;
        return true;
    } else if (event->type() == QEvent::MouseButtonPress || event->type() == QEvent::MouseButtonRelease) {
        auto* mouse = static_cast<QMouseEvent*>(event);
        const int button = mouseButton(mouse->button());
        if (button) {
            m_Session.sendMouseButton(event->type() == QEvent::MouseButtonPress, button);
            return true;
        }
    } else if (event->type() == QEvent::Wheel) {
        auto* wheel = static_cast<QWheelEvent*>(event);
        m_Session.sendMouseScroll(short(wheel->angleDelta().y()));
        return true;
    } else if (event->type() == QEvent::FocusOut) {
        releaseKeys();
    }
    return QMainWindow::eventFilter(watched, event);
}

void ViewerWindow::refreshGamepad()
{
    if (!m_Streaming) return;
    if (!m_Gamepad || !SDL_GameControllerGetAttached(static_cast<SDL_GameController*>(m_Gamepad))) {
        if (m_Gamepad) SDL_GameControllerClose(static_cast<SDL_GameController*>(m_Gamepad));
        m_Gamepad = nullptr;
        for (int i = 0; i < SDL_NumJoysticks(); ++i) {
            if (SDL_IsGameController(i)) {
                m_Gamepad = SDL_GameControllerOpen(i);
                break;
            }
        }
        const bool connected = m_Gamepad != nullptr;
        if (connected != m_GamepadConnected) {
            m_GamepadConnected = connected;
            m_Session.setControllerConnected(connected);
        }
        if (!m_Gamepad) return;
    }
    const bool connected = m_Gamepad && SDL_GameControllerGetAttached(static_cast<SDL_GameController*>(m_Gamepad));
    if (connected != m_GamepadConnected) {
        m_GamepadConnected = connected;
        m_Session.setControllerConnected(connected);
    }
    if (!connected) return;
    SDL_GameControllerUpdate();
    auto* pad = static_cast<SDL_GameController*>(m_Gamepad);
    int buttons = 0;
    const struct { SDL_GameControllerButton sdl; int flag; } map[] = {
        {SDL_CONTROLLER_BUTTON_A, 0x1000}, {SDL_CONTROLLER_BUTTON_B, 0x2000},
        {SDL_CONTROLLER_BUTTON_X, 0x4000}, {SDL_CONTROLLER_BUTTON_Y, 0x8000},
        {SDL_CONTROLLER_BUTTON_BACK, 0x0020}, {SDL_CONTROLLER_BUTTON_START, 0x0010},
        {SDL_CONTROLLER_BUTTON_LEFTSTICK, 0x0040}, {SDL_CONTROLLER_BUTTON_RIGHTSTICK, 0x0080},
        {SDL_CONTROLLER_BUTTON_LEFTSHOULDER, 0x0100}, {SDL_CONTROLLER_BUTTON_RIGHTSHOULDER, 0x0200},
        {SDL_CONTROLLER_BUTTON_DPAD_UP, 0x0001}, {SDL_CONTROLLER_BUTTON_DPAD_DOWN, 0x0002},
        {SDL_CONTROLLER_BUTTON_DPAD_LEFT, 0x0004}, {SDL_CONTROLLER_BUTTON_DPAD_RIGHT, 0x0008}
    };
    for (const auto& item : map)
        if (SDL_GameControllerGetButton(pad, item.sdl)) buttons |= item.flag;
    const auto axis = [pad](SDL_GameControllerAxis a) {
        return short(SDL_GameControllerGetAxis(pad, a));
    };
    const auto trigger = [pad](SDL_GameControllerAxis a) {
        return static_cast<unsigned char>((int(SDL_GameControllerGetAxis(pad, a)) + 32768) / 256);
    };
    m_Session.sendControllerState(buttons, trigger(SDL_CONTROLLER_AXIS_TRIGGERLEFT),
                                  trigger(SDL_CONTROLLER_AXIS_TRIGGERRIGHT),
                                  axis(SDL_CONTROLLER_AXIS_LEFTX), axis(SDL_CONTROLLER_AXIS_LEFTY),
                                  axis(SDL_CONTROLLER_AXIS_RIGHTX), axis(SDL_CONTROLLER_AXIS_RIGHTY));
}
