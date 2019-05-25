#pragma once

#include <QObject>
#include <QIODevice>
#include <QUrl>
#include <QPointer>
#include <QJsonObject>
#include <QJsonArray>
#include <QJSValue>
#include <functional>

class QBackendObject;
class QQmlEngine;

class QBackendRemoteObject : public QObject
{
public:
    QBackendRemoteObject(QObject *parent = nullptr) : QObject(parent) { }

    virtual QObject *object() const = 0;

    // Called when an object has been associated with the subscribed identifier
    virtual void objectFound(const QJsonObject& object) = 0;

    // Called when a method is invoked on this object
    virtual void methodInvoked(const QString& method, const QJsonArray& params) = 0;

    // Called with the return value from a previously invoked method
    virtual void methodReturned(const QByteArray& returnId, const QJsonValue& value, bool isError) = 0;
};

class QBackendConnection : public QObject
{
    Q_OBJECT
    Q_PROPERTY(QUrl url READ url WRITE setUrl NOTIFY urlChanged)

public:
    QBackendConnection(QObject *parent = nullptr);
    QBackendConnection(QQmlEngine *engine);

    QQmlEngine *qmlEngine() const;
    void setQmlEngine(QQmlEngine *engine);

    QUrl url() const;
    void setUrl(const QUrl& url);

    Q_INVOKABLE QObject *object(const QByteArray &identifier) const;
    QObject *ensureObject(const QJsonObject &object);
    QObject *ensureObject(const QByteArray &identifier, const QJsonObject &type);
    QJSValue ensureJSObject(const QJsonObject &object);
    QJSValue ensureJSObject(const QByteArray &identifier, const QJsonObject &type);

    void registerTypes(const char *uri);

    void invokeMethod(const QByteArray& objectIdentifier, const QString& method, const QJsonArray& params);
    QByteArray invokeMethodWithReturn(const QByteArray& objectIdentifier, const QString& method, const QJsonArray& params);
    void addObjectProxy(const QByteArray& identifier, QBackendRemoteObject* object);
    void addObjectInstantiated(const QString &typeName, const QByteArray& identifier, QBackendRemoteObject* object);
    void removeObject(const QByteArray& identifier, QBackendRemoteObject *object);
    void resetObjectData(const QByteArray& identifier, bool synchronous = false);

    void moveToThread(QThread *thread);

    // Make this private once blocking invoke exists
    QJsonObject waitForMessage(const char* waitType, std::function<bool(const QJsonObject&)> callback);

    QMetaObject *newTypeMetaObject(const QJsonObject &type);

signals:
    void urlChanged();
    void ready();

protected:
    void setBackendIo(QIODevice *read, QIODevice *write);

private slots:
    void handleDataReady();

private:
    // Try qmlEngine also; this is for singletons or other contexts where engine is explicit
    QQmlEngine *m_qmlEngine = nullptr;

    QUrl m_url;
    QIODevice *m_readIo = nullptr;
    QIODevice *m_writeIo = nullptr;
    QByteArray m_msgBuf;
    QList<QByteArray> m_pendingData;
    int m_version = 0;

    bool ensureConnectionConfig();
    bool ensureConnectionInit();

    void handleMessage(const QByteArray &message);
    void handleMessage(const QJsonObject &message);
    void handlePendingMessages();
    void write(const QJsonObject &message);

    void connectionError(const QString &context);

    enum class ConnectionState {
        // Pre-VERSION
        WantVersion,
        // Pre-CREATABLE_TYPES
        WantTypes,
        // Want a QML engine pointer
        WantEngine,
        // Ready to handle messages
        Ready
    };
    ConnectionState m_state = ConnectionState::WantVersion;
    void setState(ConnectionState newState);

    QList<QJsonObject> m_pendingMessages;
    std::function<bool(const QJsonObject&)> m_syncCallback;
    QJsonObject m_syncResult;

    // Hash of identifier -> proxy object for all existing objects
    QHash<QByteArray,QBackendRemoteObject*> m_objects;
    QJsonArray m_creatableTypes;

    QHash<QString,QMetaObject*> m_typeCache;
};

