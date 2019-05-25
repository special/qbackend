#include "plugin.h"
#include <QQmlEngine>
#include <QCoreApplication>

#include "qbackendconnection.h"
#include "qbackendprocess.h"
#include "qbackendobject.h"
#include "qbackendmodel.h"

static QBackendConnection *singleConnection = nullptr;

void QBackendPlugin::registerTypes(const char *uri)
{
    qRegisterMetaType<QBackendObject*>();
    qRegisterMetaType<QBackendModel*>();

    if (QByteArray(uri) == "Crimson.QBackend") {
        // Make the connection immediately, so it will have an opportunity to register
        // types dynamically.
        Q_ASSERT(!singleConnection);
        singleConnection = new QBackendConnection;

        // This is delicate, but I think it's safe.
        //
        // This is executing on the QML type loader thread right now. The connection needs
        // to be moved after the type registration, along with its QIODevices.
        //
        // To do this, the connection will (synchronously) block until type
        // registration is complete, and we then move the connection along with
        // its children to the main thread.
        singleConnection->registerTypes(uri);
        singleConnection->moveToThread(QCoreApplication::instance()->thread());
    } else if (QByteArray(uri) == "Crimson.QBackend.Connection") {
        // QBackend.Connection exposes explicit types for the connection, including a
        // type to execute a new process for the backend.
        qmlRegisterType<QBackendConnection>(uri, 1, 0, "BackendConnection");
        qmlRegisterType<QBackendProcess>(uri, 1, 0, "BackendProcess");
    } else {
        Q_ASSERT_X(false, "QBackendPlugin", "unexpected plugin URI");
    }
}

