#pragma once

#include <QMetaObject>
#include <QJsonObject>
#include <QQmlEngine>
#include <QJSEngine>
#include <QtCore/private/qmetaobjectbuilder_p.h>
#include "qbackendobject_p.h"

Q_DECLARE_LOGGING_CATEGORY(lcConnection)

class QBackendConnection;

/* InstantiableBackendType is a wrapper around T (QBackendObject or QBackendModel)
 * to allow registering dynamic types as instantiable QML types.
 *
 * qmlRegisterType expects a unique actual type for each registered type. It's
 * possible to get around some of this, but ultimately ends up frustrated by not
 * having a way to pass data about the type to the create function.
 *
 * addInstantiableBackendType keeps a counter of registered types and provides each
 * a unique type using the I template argument. That type is setup with the
 * connection and typeinfo, and generates a dummy staticMetaObject before registering
 * as a QML type. The constructor can then pass the correct typeinfo on to the base
 * class, building a normal object.
 *
 * The template must be statically instantiated, so there is a limit of 10 registered
 * types per T, defined by addInstantiableBackendType. This limit is shared by all
 * connections, and they are not smart enough to reuse identical types.
 */

template<typename T, int I> class InstantiableBackendType : public T
{
public:
    static QMetaObject staticMetaObject;

    static void setupType(const char *uri, QBackendConnection *connection, const QJsonObject &type)
    {
        Q_ASSERT(!m_connection);
        m_connection = connection;
        m_type = type;

        staticMetaObject = *metaObjectFromType(type, &T::staticMetaObject);

        if (type.value("_qb_singleton").isUndefined()) {
            qmlRegisterType<InstantiableBackendType<T,I>>(uri, 1, 0, staticMetaObject.className());
        } else {
            qmlRegisterSingletonType(uri, 1, 0, staticMetaObject.className(), [](QQmlEngine *engine, QJSEngine *scriptEngine) -> QJSValue {
                    Q_UNUSED(engine)

                    return scriptEngine->newQObject(staticMetaObject.newInstance());
            });
        }

        qCDebug(lcConnection) << "Registered instantiable type" << staticMetaObject.className();
    }

    InstantiableBackendType()
        : T(m_connection, instanceMetaObject())
    {
        Q_ASSERT(m_connection);
        qCDebug(lcConnection) << "Constructed an instantiable" << staticMetaObject.className() << "with id" << this->property("_qb_identifier").toString();
    }

private:
    static QBackendConnection *m_connection;
    static QJsonObject m_type;

    QMetaObject *instanceMetaObject()
    {
        QMetaObjectBuilder b(&staticMetaObject);
        b.setSuperClass(&T::staticMetaObject);
        return b.toMetaObject();
    }
};

template<typename T, int I> QMetaObject InstantiableBackendType<T,I>::staticMetaObject;
template<typename T, int I> QBackendConnection *InstantiableBackendType<T,I>::m_connection;
template<typename T, int I> QJsonObject InstantiableBackendType<T,I>::m_type;

template<typename T> void addInstantiableBackendType(const char *uri, QBackendConnection *c, const QJsonObject &type)
{
    static int i;
    switch (i) {
    case 0:
        InstantiableBackendType<T,0>::setupType(uri, c, type);
        break;
    case 1:
        InstantiableBackendType<T,1>::setupType(uri, c, type);
        break;
    case 2:
        InstantiableBackendType<T,2>::setupType(uri, c, type);
        break;
    case 3:
        InstantiableBackendType<T,3>::setupType(uri, c, type);
        break;
    case 4:
        InstantiableBackendType<T,4>::setupType(uri, c, type);
        break;
    case 5:
        InstantiableBackendType<T,5>::setupType(uri, c, type);
        break;
    case 6:
        InstantiableBackendType<T,6>::setupType(uri, c, type);
        break;
    case 7:
        InstantiableBackendType<T,7>::setupType(uri, c, type);
        break;
    case 8:
        InstantiableBackendType<T,8>::setupType(uri, c, type);
        break;
    case 9:
        InstantiableBackendType<T,9>::setupType(uri, c, type);
        break;
    default:
        qCCritical(lcConnection) << "Backend has registered too many instantiable types." << type.value("name").toString() << "discarded.";
        return;
    }
    i++;
}

