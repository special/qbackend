#include <QDebug>
#include <QJsonDocument>
#include <QJsonArray>
#include <QJsonObject>
#include <QLoggingCategory>
#include <QAbstractSocket>
#include <QQmlEngine>
#include <QQmlContext>
#include <QCoreApplication>
#include <QElapsedTimer>
#include <QUuid>

#include "qbackendconnection.h"
#include "qbackendobject.h"
#include "qbackendmodel.h"
#include "instantiable.h"

// #define PROTO_DEBUG

Q_LOGGING_CATEGORY(lcConnection, "backend.connection")
Q_LOGGING_CATEGORY(lcProto, "backend.proto")
Q_LOGGING_CATEGORY(lcProtoExtreme, "backend.proto.extreme", QtWarningMsg)

QBackendConnection::QBackendConnection(QObject *parent)
    : QObject(parent)
{
}

QBackendConnection::QBackendConnection(QQmlEngine *engine)
    : QObject()
    , m_qmlEngine(engine)
{
}

// When QBackendConnection is a singleton, qmlEngine/qmlContext may not always work.
// This will return the explicit engine as well, if one is known.
QQmlEngine *QBackendConnection::qmlEngine() const
{
    return m_qmlEngine ? m_qmlEngine : ::qmlEngine(this);
}

void QBackendConnection::setQmlEngine(QQmlEngine *engine)
{
    if (m_qmlEngine == engine) {
        return;
    }
    if (m_qmlEngine && m_qmlEngine != engine) {
        Q_ASSERT(false);
        qCritical(lcConnection) << "Backend connection is reused by another QML engine. This will go badly.";
        return;
    }

    m_qmlEngine = engine;
    setState(ConnectionState::Ready);
}

QUrl QBackendConnection::url() const
{
    return m_url;
}

void QBackendConnection::setUrl(const QUrl& url)
{
    m_url = url;
    emit urlChanged();

    qCInfo(lcConnection) << "Opening URL" << url;

    if (url.scheme() == "fd") {
        // fd:0 (rw) or fd:0,1 (r,w)
        QStringList values = url.path().split(",");
        int rdFd = -1, wrFd = -1;
        bool ok = false;
        if (values.size() == 2) {
            rdFd = values[0].toInt(&ok);
            if (!ok)
                rdFd = -1;
            wrFd = values[1].toInt(&ok);
            if (!ok)
                wrFd = -1;
        } else if (values.size() == 1) {
            rdFd = wrFd = values[0].toInt(&ok);
            if (!ok)
                rdFd = wrFd = -1;
        }

        if (rdFd < 0 || wrFd < 0) {
            qCritical() << "Invalid QBackendConnection url" << url;
            return;
        }

        QAbstractSocket *rd = new QAbstractSocket(QAbstractSocket::UnknownSocketType, this);
        if (!rd->setSocketDescriptor(rdFd)) {
            qCritical() << "QBackendConnection failed for read fd:" << rd->errorString();
            return;
        }

        QAbstractSocket *wr = nullptr;
        if (rdFd == wrFd) {
            wr = rd;
        } else {
            wr = new QAbstractSocket(QAbstractSocket::UnknownSocketType, this);
            if (!wr->setSocketDescriptor(wrFd)) {
                qCritical() << "QBackendConnection failed for write fd:" << wr->errorString();
                return;
            }
        }

        setBackendIo(rd, wr);
    } else {
        qCritical() << "Unknown QBackendConnection scheme" << url.scheme();
        return;
    }
}

void QBackendConnection::setBackendIo(QIODevice *rd, QIODevice *wr)
{
    if (m_readIo || m_writeIo) {
        qFatal("QBackendConnection IO cannot be reset");
        return;
    }

    m_readIo = rd;
    m_writeIo = wr;

    if (m_pendingData.length()) {
        for (const QByteArray& data : m_pendingData) {
            if (m_writeIo->write(data) < 0) {
                connectionError("flush pending data");
                return;
            }
        }
        m_pendingData.clear();
    }

    connect(m_readIo, &QIODevice::readyRead, this, &QBackendConnection::handleDataReady);
    handleDataReady();
}

void QBackendConnection::moveToThread(QThread *thread)
{
    QObject::moveToThread(thread);
    if (m_readIo)
        m_readIo->moveToThread(thread);
    if (m_writeIo)
        m_writeIo->moveToThread(thread);
}

bool QBackendConnection::ensureConnectionConfig()
{
    if (!m_url.isEmpty()) {
        return true;
    }

    // Try to setup connection from the QML context, cmdline, and environment, in that order
    QQmlContext *context = qmlContext(this);
    if (!context && m_qmlEngine) {
        context = m_qmlEngine->rootContext();
    }
    if (context) {
        QString url = context->contextProperty("qbackendUrl").toString();
        if (!url.isEmpty()) {
            qCDebug(lcConnection) << "Configuring connection URL from" << (qmlContext(this) ? "object" : "root") << "context property";
            setUrl(url);
            return true;
        }
    } else {
        qCDebug(lcConnection) << "No context associated with connection object, skipping context configuration";
    }

    QStringList args = QCoreApplication::arguments();
    int argp = args.indexOf("-qbackend");
    if (argp >= 0 && argp+1 < args.size()) {
        qCDebug(lcConnection) << "Configuring connection URL from commandline";
        setUrl(args[argp+1]);
        return true;
    }

    QString env = qEnvironmentVariable("QBACKEND_URL");
    if (!env.isEmpty()) {
        qCDebug(lcConnection) << "Configuring connection URL from environment";
        setUrl(env);
        return true;
    }

    return false;
}

bool QBackendConnection::ensureConnectionInit()
{
    if (!ensureConnectionConfig())
        return false;
    if (!m_readIo || !m_readIo->isOpen() || !m_writeIo || !m_writeIo->isOpen())
        return false;
    if (m_version)
        return true;

    QElapsedTimer tm;
    qCDebug(lcConnection) << "Blocking until backend connection is ready";
    tm.restart();

    waitForMessage("version", [](const QJsonObject &msg) { return msg.value("command").toString() == "VERSION"; });
    Q_ASSERT(m_version);

    qCDebug(lcConnection) << "Blocked for" << tm.elapsed() << "ms to initialize connection";
    return m_version;
}

// Register instantiable types with the QML engine, blocking if necessary
void QBackendConnection::registerTypes(const char *uri)
{
    if (!ensureConnectionInit()) {
        qCCritical(lcConnection) << "Connection initialization failed, cannot register types";
        return;
    }
    Q_ASSERT(m_state != ConnectionState::WantVersion);

    // Don't block if we already have types
    if (m_state == ConnectionState::WantTypes) {
        QElapsedTimer tm;
        qCDebug(lcConnection) << "Blocking to initialize creatable types";
        tm.restart();

        waitForMessage("creatable_types", [](const QJsonObject &msg) { return msg.value("command").toString() == "CREATABLE_TYPES"; });

        qCDebug(lcConnection) << "Blocked for" << tm.elapsed() << "ms for creatable types";
    }

    for (const QJsonValue &v : qAsConst(m_creatableTypes)) {
        QJsonObject type = v.toObject();
        // See instantiable.h for an explanation of how this magic works
        if (!type.value("properties").toObject().value("_qb_model").isUndefined())
            addInstantiableBackendType<QBackendModel>(uri, this, type);
        else
            addInstantiableBackendType<QBackendObject>(uri, this, type);
    }
}

/* I gift to you a brief, possibly accurate protocol description.
 *
 * == Protocol framing ==
 * All messages begin with an ASCII-encoded integer greater than 0, followed by a space.
 * This is followed by a message blob of exactly that size, then by a newline (which is not
 * included in the blob size). That is:
 *
 *   "<int:size> <blob(size):message>\n"
 *
 * The message blob can contain newlines, so don't try to parse based on those.
 *
 * == Messages ==
 * Messages themselves are JSON objects. The only mandatory field is "command", all others
 * are command specific.
 *
 *   { "command": "VERSION", ... }
 *
 * == Commands ==
 * RTFS. Backend is expected to send VERSION, and CREATABLE_TYPES immediately, in
 * that order, unconditionally.
 */

void QBackendConnection::handleDataReady()
{
    int rdSize = m_readIo->bytesAvailable();
    if (rdSize < 1) {
        return;
    }

    int p = m_msgBuf.size();
    m_msgBuf.resize(p+rdSize);

    rdSize = m_readIo->read(m_msgBuf.data()+p, qint64(rdSize));
    if (rdSize < 0 || (rdSize == 0 && !m_readIo->isOpen())) {
        connectionError("read error");
        return;
    } else if (p+rdSize < m_msgBuf.size()) {
        m_msgBuf.resize(p+rdSize);
    }

    while (m_msgBuf.size() >= 2) {
        int headSz = m_msgBuf.indexOf(' ');
        if (headSz < 1) {
            if (headSz == 0) {
                // Everything has gone wrong
                qCDebug(lcConnection) << "Invalid data on connection:" << m_msgBuf;
                connectionError("invalid data");
            }
            // Otherwise, there's just not a full size yet
            return;
        }

        bool szOk = false;
        int blobSz = m_msgBuf.mid(0, headSz).toInt(&szOk);
        if (!szOk || blobSz < 1) {
            // Also everything has gone wrong
            qCDebug(lcConnection) << "Invalid data on connection:" << m_msgBuf;
            connectionError("invalid data");
            return;
        }
        // Include space in headSz now
        headSz++;

        // Wait for headSz + blobSz + 1 (the newline) bytes
        if (m_msgBuf.size() < headSz + blobSz + 1) {
            return;
        }

        // Skip past headSz, then read blob and trim newline
        QByteArray message = m_msgBuf.mid(headSz, blobSz);
        m_msgBuf.remove(0, headSz+blobSz+1);

        handleMessage(message);
    }
}

void QBackendConnection::connectionError(const QString &context)
{
    qCCritical(lcConnection) << "Connection failed during" << context <<
        ": (read: " << (m_readIo ? m_readIo->errorString() : "null") << ") "
        "(write: " << (m_writeIo ? m_writeIo->errorString() : "null") << ")";
    m_readIo->close();
    m_writeIo->close();
    qFatal("backend failed");
}

void QBackendConnection::handleMessage(const QByteArray &message)
{
#if defined(PROTO_DEBUG)
    qCDebug(lcProto) << "Read " << message;
#endif

    QJsonParseError pe;
    QJsonDocument json = QJsonDocument::fromJson(message, &pe);
    if (!json.isObject()) {
        qCWarning(lcProto) << "bad message:" << message << pe.errorString();
        connectionError("bad message");
        return;
    }

    handleMessage(json.object());
}

void QBackendConnection::setState(ConnectionState newState)
{
    if (newState == m_state) {
        return;
    }

    auto oldState = m_state;
    m_state = newState;

    switch (m_state) {
    case ConnectionState::WantVersion:
        qCDebug(lcConnection) << "State -- want version.";
        break;
    case ConnectionState::WantTypes:
        Q_ASSERT(oldState == ConnectionState::WantVersion);
        qCDebug(lcConnection) << "State -- Got version. Want types.";
        break;
    case ConnectionState::WantEngine:
        Q_ASSERT(oldState == ConnectionState::WantTypes);
        if (m_qmlEngine) {
            // immediately transition
            setState(ConnectionState::Ready);
        } else {
            qCDebug(lcConnection) << "State -- Got types. Want engine.";
        }
        break;
    case ConnectionState::Ready:
        Q_ASSERT(m_qmlEngine);
        Q_ASSERT(oldState == ConnectionState::WantEngine);
        qCDebug(lcConnection) << "State -- Entered established state. Flushing pending.";
        break;
    }

    handlePendingMessages();
}

void QBackendConnection::handleMessage(const QJsonObject &cmd)
{
    QString command = cmd.value("command").toString();
    bool doDeliver = true;

    if (!m_syncResult.isEmpty()) {
        qCDebug(lcConnection) << "Queueing handling of " << command << " due to syncResult";
        doDeliver = false;
    } else if (m_state != ConnectionState::Ready) {
        // VERSION and CREATABLE_TYPES must happen before anything else, and nothing
        // else could be handled until there is a QML engine. Queue all other messages.
        if (m_state == ConnectionState::WantVersion && command == "VERSION")
            doDeliver = true;
        else if (m_state == ConnectionState::WantTypes && command == "CREATABLE_TYPES")
            doDeliver = true;
        else
            doDeliver = false;
    }

    if (doDeliver && m_syncCallback && !m_syncCallback(cmd)) {
        // If we're blocking for a message and it's not this message, queue it
        doDeliver = false;
    }

    if (!doDeliver) {
        qCDebug(lcConnection) << "Queuing handling of" << command << cmd;
        m_pendingMessages.append(cmd);
        return;
    }

    if (m_syncCallback)
        m_syncResult = cmd;

    if (command == "VERSION") {
        Q_ASSERT(m_state == ConnectionState::WantVersion);
        m_version = cmd.value("version").toInt();
        qCInfo(lcConnection) << "Connected to backend version" << m_version;
        setState(ConnectionState::WantTypes);
    } else if (command == "CREATABLE_TYPES") {
        Q_ASSERT(m_state == ConnectionState::WantTypes);
        m_creatableTypes = cmd.value("types").toArray();
        setState(ConnectionState::WantEngine);
    } else if (command == "OBJECT_RESET") {
        QByteArray identifier = cmd.value("identifier").toString().toUtf8();
        auto obj = m_objects.value(identifier);
        if (obj) {
            obj->objectFound(cmd.value("data").toObject());
        }
    } else if (command == "EMIT") {
        QByteArray identifier = cmd.value("identifier").toString().toUtf8();
        QString method = cmd.value("method").toString();
        QJsonArray params = cmd.value("parameters").toArray();

        qCDebug(lcConnection) << "Emit " << method << " on " << identifier << params;
        auto obj = m_objects.value(identifier);
        if (obj) {
            obj->methodInvoked(method, params);
        }
    } else if (command == "INVOKE_RETURN") {
        QByteArray objectId = cmd.value("identifier").toString().toUtf8();
        QByteArray returnId = cmd.value("return").toString().toUtf8();
        auto obj = m_objects.value(objectId);

        if (obj) {
            auto error = cmd.value("error");
            if (!error.isUndefined()) {
                qCDebug(lcConnection) << "Invoked call" << returnId << "returned error:" << error;
                obj->methodReturned(returnId, error, true);
            } else {
                qCDebug(lcConnection) << "Invoked call" << returnId << "returned:" << cmd.value("value");
                obj->methodReturned(returnId, cmd.value("value"), false);
            }
        }
    } else {
        qCWarning(lcConnection) << "Unknown command" << command << "from backend";
        connectionError("unknown command");
    }
}

void QBackendConnection::handlePendingMessages()
{
    const auto pending = m_pendingMessages;
    m_pendingMessages.clear();
    if (pending.isEmpty()) {
        return;
    }

    qCDebug(lcConnection) << "Handling" << pending.size() << "queued messages";
    for (const QJsonObject &msg : pending) {
        handleMessage(msg);
    }
}

void QBackendConnection::write(const QJsonObject &message)
{
    QByteArray data = QJsonDocument(message).toJson(QJsonDocument::Compact);
    data = QByteArray::number(data.size()) + " " + data + "\n";

    if (!m_writeIo) {
        qCDebug(lcProtoExtreme) << "Write on an inactive connection buffered: " << data;
        m_pendingData.append(data);
        return;
    }

#if defined(PROTO_DEBUG)
    qCDebug(lcProto) << "Writing " << data;
#endif
    if (m_writeIo->write(data) < 0) {
        connectionError("write");
    }
}

// waitForMessage blocks and reads messages from the connection, passing each to the callback
// function until it returns true. The selected message is returned.
//
// Any other messages (returning false from the callback) will be queued to handle normally
// later. They will not have been handled when this function returns; the selected message is
// taken out of order.
//
// waitForMessage is safe to call recursively (for different messages), even if those messages
// arrive out of order.
QJsonObject QBackendConnection::waitForMessage(const char *waitType, std::function<bool(const QJsonObject&)> callback)
{
    // Flush write buffer before blocking
    while (m_writeIo->bytesToWrite() > 0) {
        if (!m_writeIo->waitForBytesWritten(5000)) {
            connectionError("synchronous write");
            return QJsonObject();
        }
    }

    qCDebug(lcConnection) << "Waiting for " << waitType;

    // waitForMessage can be called recursively (through handleDataReady). Save the
    // existing syncCallback and syncResult here, and restore them before returning.
    auto savedResult = m_syncResult;
    auto savedCallback = m_syncCallback;
    m_syncResult = QJsonObject();
    m_syncCallback = callback;

    // Flush pending messages, in case one of these is matched by the callback.
    handlePendingMessages();

    while (m_syncResult.isEmpty()) {
        if (!m_readIo->waitForReadyRead(5000)) {
            connectionError("synchronous read");
            break;
        }
        handleDataReady();
    }

    QJsonObject re = m_syncResult;
    m_syncResult = savedResult;
    m_syncCallback = savedCallback;
    qCDebug(lcConnection) << "Finished waiting for " << waitType;

    // Check pending messages the next time around, after the caller has a chance to react
    if (!m_pendingMessages.isEmpty())
        QMetaObject::invokeMethod(this, &QBackendConnection::handlePendingMessages, Qt::QueuedConnection);
    return re;
}

void QBackendConnection::invokeMethod(const QByteArray& objectIdentifier, const QString& method, const QJsonArray& params)
{
    qCDebug(lcConnection) << "Invoking " << objectIdentifier << method << params;
    write(QJsonObject{
          {"command", "INVOKE"},
          {"identifier", QString::fromUtf8(objectIdentifier)},
          {"method", method},
          {"parameters", params}
    });
}

QByteArray QBackendConnection::invokeMethodWithReturn(const QByteArray& objectIdentifier, const QString& method, const QJsonArray& params)
{
    auto returnId = QUuid::createUuid().toString();
    qCDebug(lcConnection) << "Invoking returnable call" << returnId << "on object" << objectIdentifier << method << params;
    write(QJsonObject{
          {"command", "INVOKE"},
          {"identifier", QString::fromUtf8(objectIdentifier)},
          {"return", returnId},
          {"method", method},
          {"parameters", params}
    });
    return returnId.toUtf8();
}

void QBackendConnection::addObjectProxy(const QByteArray& identifier, QBackendRemoteObject* proxy)
{
    if (m_objects.contains(identifier)) {
        qCWarning(lcConnection) << "Duplicate object identifiers on connection for objects" << proxy << "and" << m_objects.value(identifier);
        return;
    }

    qCDebug(lcConnection) << "Creating remote object handler " << identifier << " on connection " << this << " for " << proxy;
    m_objects.insert(identifier, proxy);

    write(QJsonObject{
          {"command", "OBJECT_REF"},
          {"identifier", QString::fromUtf8(identifier)},
    });
}

void QBackendConnection::addObjectInstantiated(const QString &typeName, const QByteArray &identifier, QBackendRemoteObject *proxy)
{
    m_objects.insert(identifier, proxy);
    write(QJsonObject{
          {"command", "OBJECT_CREATE"},
          {"typeName", typeName},
          {"identifier", QString::fromUtf8(identifier)}
    });
}

void QBackendConnection::resetObjectData(const QByteArray& identifier, bool synchronous)
{
    write(QJsonObject{{"command", "OBJECT_QUERY"}, {"identifier", QString::fromUtf8(identifier)}});

    if (synchronous) {
        waitForMessage("object_reset", [identifier](const QJsonObject &message) -> bool {
            if (message.value("command").toString() != "OBJECT_RESET")
                return false;
            return message.value("identifier").toString().toUtf8() == identifier;
        });
    }
}

void QBackendConnection::removeObject(const QByteArray& identifier, QBackendRemoteObject *expectedObj)
{
    QBackendRemoteObject *obj = m_objects.value(identifier);
    if (!obj) {
        qCWarning(lcConnection) << "Removing object identifier" << identifier << "on connection" << this << "which isn't in list";
        return;
    } else if (obj != expectedObj) {
        qCDebug(lcConnection) << "Ignoring remove of object" << identifier << "because expected object" << expectedObj << "does not match" << obj;
        // This can happen naturally, e.g. for the case described in ensureJSObject. It's ok to ignore.
        return;
    }

    qCDebug(lcConnection) << "Removing remote object handler " << identifier << " on connection " << this << " for ";
    m_objects.remove(identifier);

    write(QJsonObject{
          {"command", "OBJECT_DEREF"},
          {"identifier", QString::fromUtf8(identifier)}
    });
}

QObject *QBackendConnection::object(const QByteArray &identifier) const
{
    auto obj = m_objects.value(identifier);
    if (obj)
        return obj->object();
    return nullptr;
}

// Create or return the backend object described by `object`, which is in the
// "_qbackend_": "object" format described in qbackendobject.cpp.
QObject *QBackendConnection::ensureObject(const QJsonObject &data)
{
    return ensureObject(data.value("identifier").toString().toUtf8(), data.value("type").toObject());
}

QObject *QBackendConnection::ensureObject(const QByteArray &identifier, const QJsonObject &type)
{
    if (identifier.isEmpty())
        return nullptr;

    auto proxyObject = m_objects.value(identifier);
    if (!proxyObject) {
        QMetaObject *metaObject = newTypeMetaObject(type);
        QObject *object;

        if (metaObject->inherits(&QAbstractListModel::staticMetaObject))
            object = new QBackendModel(this, identifier, metaObject);
        else
            object = new QBackendObject(this, identifier, metaObject);
        QQmlEngine::setContextForObject(object, qmlContext(this));
        // This should be the result of the heuristic, but I never trust it.
        QQmlEngine::setObjectOwnership(object, QQmlEngine::JavaScriptOwnership);

        // Object constructor should have registered its proxy
        proxyObject = m_objects.value(identifier);
        Q_ASSERT(proxyObject);
    }

    return proxyObject->object();
}

QJSValue QBackendConnection::ensureJSObject(const QJsonObject &data)
{
    return ensureJSObject(data.value("identifier").toString().toUtf8(), data.value("type").toObject());
}

// ensureJSObject is equivalent to ensureObject, but returns a QJSValue wrapping that object.
// This should be used instead of calling newQObject directly, because it covers corner cases.
QJSValue QBackendConnection::ensureJSObject(const QByteArray &identifier, const QJsonObject &type)
{
    QObject *obj = ensureObject(identifier, type);
    if (!obj)
        return QJSValue(QJSValue::NullValue);

    QJSValue val = qmlEngine()->newQObject(obj);
    if (!val.isQObject()) {
        // This can happen if obj was queued for deletion by the JS engine but has not yet
        // been deleted. ~BackendObjectPrivate won't have run, so ensureObject will still
        // return the same soon-to-be-dead instance.
        //
        // This is safe because removeObject won't deref the old object, because it doesn't
        // match. The duplicate OBJECT_REF is ignored, because it is a boolean reference,
        // not a reference counter.
        qCDebug(lcConnection) << "Replacing object" << identifier << "because the existing"
            << "instance was queued for deletion by JS";
        m_objects.remove(identifier);
        obj = ensureObject(identifier, type);
        if (obj)
            val = qmlEngine()->newQObject(obj);
        if (!val.isQObject())
            return QJSValue(QJSValue::NullValue);
    }

    return val;
}

QMetaObject *QBackendConnection::newTypeMetaObject(const QJsonObject &type)
{
    QMetaObject *mo = m_typeCache.value(type.value("name").toString());
    if (!mo) {
        if (type.value("omitted").toBool()) {
            // Type does not contain the full description, backend expected it to be cached.
            qCWarning(lcConnection) << "Expected cached type description for" << type.value("name").toString() << "to create object";
            // This is a bug, but allow it to continue as an object with no properties
        }

        // If type is a model type, set a superclass as well
        if (!type.value("properties").toObject().value("_qb_model").isUndefined()) {
            mo = metaObjectFromType(type, &QAbstractListModel::staticMetaObject);
        } else {
            mo = metaObjectFromType(type, nullptr);
        }

        m_typeCache.insert(type.value("name").toString(), mo);
        qDebug(lcConnection) << "Cached metaobject for type" << type.value("name").toString();
    }

    // Return a copy of the cached metaobject
    QMetaObjectBuilder b(mo);
    return b.toMetaObject();
}
