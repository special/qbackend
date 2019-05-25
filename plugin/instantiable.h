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

        qmlRegisterType<InstantiableBackendType<T,I>>(uri, 1, 0, staticMetaObject.className());

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
    // All of these are compile-time template instantiations; they come at a cost even if not used at runtime.
    static constexpr int maxTypes = 10;
    addInstantiableBackendType<T,maxTypes>(uri, c, type);
}

template<typename T, int I, typename std::enable_if<(I>0), int>::type = 0> void addInstantiableBackendType(const char *uri, QBackendConnection *c, const QJsonObject &type)
{
    static bool used;
    if (used) {
        addInstantiableBackendType<T,I-1>(uri, c, type);
    } else {
        used = true;
        InstantiableBackendType<T,I>::setupType(uri, c, type);
    }
}

template<typename T, int I, typename std::enable_if<I==0, int>::type = 0> void addInstantiableBackendType(const char *uri, QBackendConnection *c, const QJsonObject &type)
{
    Q_UNUSED(uri);
    Q_UNUSED(c);
    qCCritical(lcConnection) << "Backend has registered too many instantiable types. Type" << type.value("name").toString() << "and all future types will be discarded.";
}

// Even more than with instantiables, there's no reason to require a real type for a singleton.
// The qmlRegisterSingletonType API, however, doesn't allow for it: the callback to create an
// instance must be a bare function pointer, which rules out capturing lambdas and anything
// else that could determine which singleton to create.
//
// So as with the instantiable types, back each singleton with an actual static type, effectively
// creating N (defined at compile time) different functions that can be passed as callbacks.
template<int I> class SingletonType
{
public:
    static QBackendConnection *c;
    static QJsonObject objectRef;

    static QJSValue create(QQmlEngine *engine, QJSEngine *scriptEngine)
    {
        Q_UNUSED(scriptEngine);
        qCDebug(lcConnection) << "Creating instance of singleton" << objectRef.value("identifier").toString();
        c->setQmlEngine(engine);
        return c->ensureJSObject(objectRef);
    }
};

template<int I> QBackendConnection *SingletonType<I>::c;
template<int I> QJsonObject SingletonType<I>::objectRef;

using SingletonCallback = QJSValue (*)(QQmlEngine*, QJSEngine*);

template<int I, typename std::enable_if<(I>0), int>::type = 0> SingletonCallback createSingleton(QBackendConnection *c, const QJsonObject &object)
{
    static bool used;
    if (used) {
        return createSingleton<I-1>(c, object);
    } else {
        used = true;
        SingletonType<I>::c = c;
        SingletonType<I>::objectRef = object;
        return &SingletonType<I>::create;
    }
}

template<int I, typename std::enable_if<I==0, int>::type = 0> SingletonCallback createSingleton(QBackendConnection *c, const QJsonObject &object)
{
    Q_UNUSED(c);
    qCCritical(lcConnection) << "Backend has registered too many singleton types. Object" << object.value("identifier").toString() << "and all future singletons will be discarded.";
    return nullptr;
}

SingletonCallback createSingleton(QBackendConnection *c, const QJsonObject &object)
{
    static constexpr int max = 10;
    return createSingleton<max>(c, object);
}

