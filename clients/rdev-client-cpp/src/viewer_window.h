#pragma once

#include "gamestream_session.h"

#include <QMainWindow>
#include <QSet>
#include <QPoint>

class QComboBox;
class QLabel;
class QLineEdit;
class QPushButton;
class QSpinBox;
class QTimer;
class QEvent;

class ViewerWindow final : public QMainWindow
{
    Q_OBJECT

public:
    explicit ViewerWindow(QWidget* parent = nullptr);
    ~ViewerWindow() override;

protected:
    bool eventFilter(QObject* watched, QEvent* event) override;

private:
    void setStreaming(bool active);
    void refreshGamepad();
    void releaseKeys();

    GameStreamSession m_Session;
    QLineEdit* m_Host = nullptr;
    QSpinBox* m_HttpPort = nullptr;
    QSpinBox* m_HttpsPort = nullptr;
    QLineEdit* m_Pin = nullptr;
    QComboBox* m_Apps = nullptr;
    QLabel* m_Video = nullptr;
    QLabel* m_Status = nullptr;
    QPushButton* m_Connect = nullptr;
    QPushButton* m_Pair = nullptr;
    QPushButton* m_Launch = nullptr;
    QPushButton* m_Stop = nullptr;
    QTimer* m_GamepadTimer = nullptr;
    QSet<int> m_PressedKeys;
    QPoint m_LastMousePos;
    bool m_HaveMousePos = false;
    bool m_Streaming = false;
    bool m_GamepadConnected = false;
    void* m_Gamepad = nullptr;
};
