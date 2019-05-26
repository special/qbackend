#pragma once

#include <QObject>
#include <QQmlParserStatus>

class BackendObjectPrivate;
class QBackendConnection;

class QBackendObject : public QObject, public QQmlParserStatus
{
public:
    QBackendObject(QBackendConnection *connection, QByteArray identifier, QMetaObject *metaObject, QObject *parent = nullptr);
    virtual ~QBackendObject();

    static QMetaObject staticMetaObject;
    virtual const QMetaObject *metaObject() const override;
    virtual int qt_metacall(QMetaObject::Call c, int id, void **argv) override;

    void classBegin() override;
    void componentComplete() override;

protected:
    QBackendObject(QBackendConnection *connection, QMetaObject *type);

private:
    BackendObjectPrivate *d;
    QMetaObject *m_metaObject = nullptr;
};

Q_DECLARE_METATYPE(QBackendObject*)
