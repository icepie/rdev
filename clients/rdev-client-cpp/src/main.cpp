#include "viewer_window.h"

#include <QApplication>
#include <QCoreApplication>

int main(int argc, char** argv)
{
    QApplication app(argc, argv);
    QCoreApplication::setOrganizationName(QStringLiteral("RDev"));
    QCoreApplication::setApplicationName(QStringLiteral("rdev-client-cpp"));
    ViewerWindow window;
    window.show();
    return app.exec();
}
