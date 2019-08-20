#pragma once

#include <QObject>
#include <QJsonObject>
#include <QMetaObject>
#include <QJSValue>
#include "qbackendconnection.h"

class Promise;

class BackendObjectPrivate : public QBackendRemoteObject
{
    Q_OBJECT

public:
    QObject *m_object = nullptr;

    QBackendConnection *m_connection = nullptr;
    QByteArray m_identifier;
    bool m_instantiated = false;

    QHash<QByteArray, QVariant> m_data;
    bool m_dataReady = false;
    bool m_waitingForData = false;

    QHash<QByteArray,Promise*> m_promises;

    BackendObjectPrivate(QObject *object, QBackendConnection *connection, const QByteArray &identifier);
    BackendObjectPrivate(const char *typeName, QObject *object, QBackendConnection *connection);
    virtual ~BackendObjectPrivate();

    QObject *object() const override { return m_object; }
    void methodInvoked(const QString& method, const QJsonArray& params) override;
    void methodReturned(const QByteArray& returnId, const QJsonValue& value, bool isError) override;
    void updateData(const QHash<QByteArray, QVariant> &properties, bool reset) override;

    int metacall(QMetaObject::Call c, int id, void **argv);

    void classBegin();
    void componentComplete();

    void *jsonValueToMetaArgs(QMetaType::Type type, const QJsonValue &value, void *p = nullptr);
};

QMetaObject *metaObjectFromType(const QJsonObject &type, const QMetaObject *superClass = nullptr);
