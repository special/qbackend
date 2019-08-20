#include <QDebug>
#include <QMetaObject>
#include <QMetaProperty>
#include <QJsonDocument>
#include <QJsonArray>
#include <QJsonObject>
#include <QJSValue>
#include <QLoggingCategory>
#include <QQmlComponent>
#include <QQmlEngine>
#include <QJSValueIterator>
#include <QUuid>
#include <QtCore/private/qmetaobjectbuilder_p.h>
#include "qbackendobject.h"
#include "qbackendobject_p.h"
#include "qbackendconnection.h"
#include "promise.h"

Q_LOGGING_CATEGORY(lcObject, "backend.object")

template<typename T> static void *copyMetaArg(QMetaType::Type type, void *p, const T &v);
QJsonValue jsValueToJsonValue(const QJSValue &value);

// Create a dummy staticMetaObject that provides at least the correct type name
QMetaObject QBackendObject::staticMetaObject =
[]() -> QMetaObject
{
    QMetaObjectBuilder b;
    b.setClassName("QBackendObject");
    b.setSuperClass(&QObject::staticMetaObject);
    return *b.toMetaObject();
}();

QBackendObject::QBackendObject(QBackendConnection *connection, QByteArray identifier, QMetaObject *metaObject, QObject *parent)
    : QObject(parent)
    , d(new BackendObjectPrivate(this, connection, identifier))
    , m_metaObject(metaObject)
{
}

QBackendObject::QBackendObject(QBackendConnection *connection, QMetaObject *type)
    : d(new BackendObjectPrivate(type->className(), this, connection))
    , m_metaObject(type)
{
}

QBackendObject::~QBackendObject()
{
    delete d;
    free(m_metaObject);
}

const QMetaObject *QBackendObject::metaObject() const
{
    Q_ASSERT(m_metaObject);
    return m_metaObject;
}

int QBackendObject::qt_metacall(QMetaObject::Call c, int id, void **argv)
{
    id = QObject::qt_metacall(c, id, argv);
    if (id < 0)
        return id;
    return d->metacall(c, id, argv);
}

void QBackendObject::classBegin()
{
    d->classBegin();
}

void QBackendObject::componentComplete()
{
    d->componentComplete();
}

BackendObjectPrivate::BackendObjectPrivate(QObject *object, QBackendConnection *connection, const QByteArray &identifier)
    : QBackendRemoteObject(object)
    , m_object(object)
    , m_connection(connection)
    , m_identifier(identifier)
{
    connection->addObjectProxy(identifier, this);
}

BackendObjectPrivate::BackendObjectPrivate(const char *typeName, QObject *object, QBackendConnection *connection)
    : QBackendRemoteObject(object)
    , m_object(object)
    , m_connection(connection)
    , m_instantiated(true)
{
    // Newly instantiated object, generate an identifier
    m_identifier = QUuid::createUuid().toString().toUtf8();
    connection->addObjectInstantiated(typeName, m_identifier, this);
}

BackendObjectPrivate::~BackendObjectPrivate()
{
    if (m_instantiated) {
        // Will silently fail if the method isn't implemented
        const QMetaObject *metaObject = m_object->metaObject();
        int idx = metaObject->indexOfMethod("componentDestruction()");
        if (idx >= 0) {
            metaObject->method(idx).invoke(m_object);
        }
    }
    m_connection->removeObject(m_identifier, this);
    for (auto p : m_promises)
        delete p;
}

void BackendObjectPrivate::methodInvoked(const QString &name, const QJsonArray &params)
{
    // Technically, this should find the signal by its full signature, to enable overloads.
    // Since we're mirroring a Go object, overloaded names don't really make sense, so we can
    // cheat and disallow them.
    const QMetaObject *metaObject = m_object->metaObject();
    for (int i = metaObject->methodOffset(); i < metaObject->methodCount(); i++) {
        QMetaMethod method = metaObject->method(i);
        if (method.methodType() != QMetaMethod::Signal || method.name() != name)
            continue;

        if (method.parameterCount() != params.count()) {
            qCWarning(lcObject) << "Signal" << method.name() << "emitted with incorrect parameters; expected" << method.methodSignature() << "got parameters" << params;
            break;
        }

        // Marshal arguments for the signal. [0] is for return value, which is void for signals.
        QVector<void*> argv(method.parameterCount()+1);
        for (int j = 0; j < method.parameterCount(); j++) {
            QMetaType::Type paramType = static_cast<QMetaType::Type>(method.parameterType(j));
            argv[j+1] = jsonValueToMetaArgs(paramType, params[j], nullptr);
        }

        qCDebug(lcObject) << "Emitting signal" << name << "with args" << params;
        QMetaObject::activate(m_object, i, argv.data());

        // Free parameters in argv
        for (int j = 0; j < method.parameterCount(); j++)
            QMetaType::destroy(method.parameterType(j), argv[j+1]);

        break;
    }
}

void BackendObjectPrivate::methodReturned(const QByteArray& returnId, const QJsonValue& value, bool isError)
{
    auto promise = m_promises.take(returnId);
    if (!promise)
        return;

    if (isError) {
        promise->reject(m_connection->jsonValueToJSValue(value));
    } else {
        // Unwrap the return values array if appropriate
        QJsonValue rv = value;
        QJsonArray values = rv.toArray();
        if (values.count() == 0) {
            rv = QJsonValue();
        } else if (values.count() == 1) {
            rv = values[0];
        }
        promise->resolve(m_connection->jsonValueToJSValue(rv));
    }

    // This object wraps the JS Promise object. QML never interacts with
    // the wrapper, so it's safe to delete immediately.
    delete promise;
}

void BackendObjectPrivate::classBegin()
{
    // If the connection doesn't have an engine associated yet, give it the one from this object.
    if (!m_connection->qmlEngine()) {
        qCDebug(lcObject) << "setting engine" << qmlEngine(m_object) << "for connection at object instantiation";
        m_connection->setQmlEngine(qmlEngine(m_object));
    }
}

void BackendObjectPrivate::componentComplete()
{
    // Will silently fail if the method isn't implemented
    const QMetaObject *metaObject = m_object->metaObject();
    int idx = metaObject->indexOfMethod("componentComplete()");
    if (idx >= 0) {
        metaObject->method(idx).invoke(m_object);
    }
}

void BackendObjectPrivate::updateData(const QHash<QByteArray, QVariant> &data, bool reset)
{
    const QMetaObject *metaObject = m_object->metaObject();

    if (reset) {
        qCDebug(lcObject) << "Resetting data for" << m_identifier;
        m_data = data;
        m_dataReady = true;

        // Don't emit signals for the initial query of properties; nothing could
        // have read properties before this, so it's meaningless to say that they
        // have changed.
        //
        // This is distinct from m_dataReady, because spontaneous change signals
        // should still be sent even if data hadn't been loaded before. The signals
        // are suppressed only for data in response to an OBJECT_QUERY.
        if (m_waitingForData) {
            return;
        }

        // XXX This is also wrong: any properties in the old m_dataObject that
        // aren't in object have also changed.
        for (auto it = m_data.constBegin(); it != m_data.constEnd(); it++) {
            int index = metaObject->indexOfProperty(it.key());
            if (index < 0)
                continue;
            QMetaProperty property = metaObject->property(index);
            int notifyIndex = property.notifySignalIndex();
            if (notifyIndex >= 0) {
                QMetaObject::activate(m_object, notifyIndex, nullptr);
            }
        }
    } else {
        // XXX This is not reachable/tested currently, until non-reset property updates exist
        for (auto it = m_data.constBegin(); it != m_data.constEnd(); it++) {
            m_data[it.key()] = it.value();

            int index = metaObject->indexOfProperty(it.key());
            if (index < 0) {
                qCWarning(lcObject) << "Cannot find property matching update of" << it.key() << "on" << metaObject->className();
                continue;
            }

            QMetaProperty property = metaObject->property(index);
            int notifyIndex = property.notifySignalIndex();
            if (notifyIndex >= 0)
                QMetaObject::activate(m_object, notifyIndex, nullptr);
        }
    }
}

// Called by the front m_object's qt_metacall to handle backend calls
int BackendObjectPrivate::metacall(QMetaObject::Call c, int id, void **argv)
{
    const QMetaObject *metaObject = m_object->metaObject();

    if (c == QMetaObject::ReadProperty) {
        int count = metaObject->propertyCount() - metaObject->propertyOffset();
        QMetaProperty property = metaObject->property(id + metaObject->propertyOffset());

        if (property.name() == QByteArray("_qb_identifier")) {
            jsonValueToMetaArgs(QMetaType::QString, QJsonValue(QString(m_identifier)), argv[0]);
        } else {
            if (!m_dataReady) {
                qCDebug(lcObject) << "Blocking to load data for object" << m_identifier << "from read of property" << property.name();
                m_waitingForData = true;
                m_connection->resetObjectData(m_identifier, true);
                m_waitingForData = false;
            }

            auto type = static_cast<QMetaType::Type>(property.userType());
            auto value = m_data.value(property.name());
            if (static_cast<QMetaType::Type>(value.type()) != type && !value.convert(type)) {
                qCWarning(lcObject) << "Cannot convert" << property.typeName() << "to" << value.typeName() << "for data of property" << property.name() << "on type" << metaObject->className();
                argv[0] = nullptr;
            } else {
                if (!argv[0])
                    argv[0] = QMetaType::create(type, value.data());
                else
                    argv[0] = QMetaType::construct(type, argv[0], value.data());
            }
        }

        id -= count;
    } else if (c == QMetaObject::WriteProperty) {
        int count = metaObject->propertyCount() - metaObject->propertyOffset();
        QMetaProperty property = metaObject->property(id + metaObject->propertyOffset());

        // Look for a corresponding setter method
        QString setSig = QString("set%1(%2)").arg(property.name()).arg(property.typeName());
        setSig[3] = setSig[3].toUpper();
        int methodIndex = metaObject->indexOfMethod(setSig.toUtf8());

        if (methodIndex >= 0) {
            // Turn this into an InvokeMetaMethod of the setter
            void *mArgv[] = { nullptr, argv[0] };
            metacall(QMetaObject::InvokeMetaMethod, methodIndex - metaObject->methodOffset(), mArgv);
        }

        id -= count;
    } else if (c == QMetaObject::InvokeMetaMethod) {
        int count = metaObject->methodCount() - metaObject->methodOffset();
        QMetaMethod method = metaObject->method(id + metaObject->methodOffset());

        if (method.isValid()) {
            QJsonArray args;
            for (int i = 0; i < method.parameterCount(); i++) {
                switch (method.parameterType(i)) {
                case QMetaType::Bool:
                    args.append(QJsonValue(*reinterpret_cast<bool*>(argv[i+1])));
                    break;
                case QMetaType::Double:
                    args.append(QJsonValue(*reinterpret_cast<double*>(argv[i+1])));
                    break;
                case QMetaType::Int:
                    args.append(QJsonValue(*reinterpret_cast<int*>(argv[i+1])));
                    break;
                case QMetaType::QString:
                    args.append(QJsonValue(*reinterpret_cast<QString*>(argv[i+1])));
                    break;
                case QMetaType::QVariant:
                    args.append(reinterpret_cast<QVariant*>(argv[i+1])->toJsonValue());
                    break;
                case QMetaType::QObjectStar:
                    if (!*reinterpret_cast<QObject**>(argv[i+1])) {
                        args.append(QJsonValue());
                    } else {
                        QString id = (*reinterpret_cast<QObject**>(argv[i+1]))->property("_qb_identifier").toString();
                        if (!id.isEmpty()) {
                            args.append(QJsonObject{{"_qbackend_", "object"}, {"identifier", id}});
                        }
                    }
                    break;
                default:
                    if (method.parameterType(i) == QMetaType::type("QJSValue")) {
                        args.append(jsValueToJsonValue(*reinterpret_cast<QJSValue*>(argv[i+1])));
                    } else {
                        // XXX
                    }
                    break;
                }
            }

            if (method.returnType() != QMetaType::Void && argv[0]) {
                Q_ASSERT(method.returnType() == qMetaTypeId<QJSValue>());
                Promise *p = new Promise(m_connection->qmlEngine());
                *reinterpret_cast<QJSValue*>(argv[0]) = std::move(p->value());

                auto returnId = m_connection->invokeMethodWithReturn(m_identifier, QString::fromUtf8(method.name()), args);
                m_promises.insert(returnId, p);
            } else {
                m_connection->invokeMethod(m_identifier, QString::fromUtf8(method.name()), args);
            }
        }

        id -= count;
    }

    return id;
}

QJsonValue jsValueToJsonValue(const QJSValue &value)
{
    if (value.isQObject()) {
        QObject *object = value.toQObject();
        if (!object) {
            return QJsonValue(QJsonValue::Null);
        }

        QString id = object->property("_qb_identifier").toString();
        if (!id.isEmpty()) {
            return QJsonObject{{"_qbackend_", "object"}, {"identifier", id}};
        } else {
            // XXX warn about passing non-backend objects
            return QJsonValue(QJsonValue::Undefined);
        }
    } else if (value.isObject()) {
        QJsonObject object;
        QJSValueIterator it(value);
        while (it.hasNext()) {
            it.next();
            object.insert(it.name(), jsValueToJsonValue(it.value()));
        }
        return object;
    } else if (value.isArray()) {
        QJsonArray array;
        int length = value.property("length").toInt();
        for (int i = 0; i < length; i++) {
            array.append(jsValueToJsonValue(value.property(i)));
        }
        return array;
    } else if (value.isString()) {
        return QJsonValue(value.toString());
    } else if (value.isBool()) {
        return QJsonValue(value.toBool());
    } else if (value.isNumber()) {
        return QJsonValue(value.toNumber());
    } else if (value.isNull()) {
        return QJsonValue(QJsonValue::Null);
    } else if (value.isUndefined()) {
        return QJsonValue(QJsonValue::Undefined);
    } else {
        Q_UNREACHABLE();
    }
}

// Construct a copy of 'v' (which is type 'type') at 'p', or allocate if 'p' is nullptr
template<typename T> static void *copyMetaArg(QMetaType::Type type, void *p, const T &v)
{
    if (!p)
        p = QMetaType::create(type, reinterpret_cast<const void*>(&v));
    else
        p = QMetaType::construct(type, p, reinterpret_cast<const void*>(&v));
    return p;
}

void *BackendObjectPrivate::jsonValueToMetaArgs(QMetaType::Type type, const QJsonValue &value, void *p)
{
    switch (type) {
    case QMetaType::Bool:
        p = copyMetaArg(type, p, value.toBool());
        break;

    case QMetaType::Double:
        p = copyMetaArg(type, p, value.toDouble());
        break;

    case QMetaType::Int:
        p = copyMetaArg(type, p, value.toInt());
        break;

    case QMetaType::QString:
        p = copyMetaArg(type, p, value.toString());
        break;

    case QMetaType::QVariant:
        p = copyMetaArg(type, p, value.toVariant());
        break;

    case QMetaType::QObjectStar:
        {
            QObject *v = m_connection->ensureObject(value.toObject());
            if (!p)
                p = QMetaType::create(type, reinterpret_cast<void*>(&v));
            else
                *reinterpret_cast<QObject**>(p) = v;
        }
        break;

    default:
        if (type == QMetaType::type("QJSValue")) {
            // m_object may not have been exposed to the engine yet, so use the connection's
            p = copyMetaArg(type, p, m_connection->jsonValueToJSValue(value));
        } else {
            qCWarning(lcObject) << "Unknown type" << QMetaType::typeName(type) << "in JSON value conversion";
        }
        break;
    }

    return p;
}

// Qt, QML
std::pair<QString,QString> qtTypesFromType(const QString &type)
{
    if (type == "string")
        return {"QString","string"};
    else if (type == "int")
        return {"int","int"};
    else if (type == "double")
        return {"double","double"};
    else if (type == "bool")
        return {"bool","bool"};
    else if (type == "object")
        return {"QObject*","var"};
    else if (type == "array")
        return {"QJSValue","var"};
    else if (type == "map")
        return {"QJSValue","var"};
    else
        return {"QJSValue","var"};
}

/* Type definitions:
 *
 * {
 *   "name": "Person",
 *   "properties": {
 *     "fullName": "string",
 *     "id": { "type": "int", "readonly": true }
 *   },
 *   "methods": {
 *     "greet": { "args": [ "string", "bool" ], "return": [ "string" ] }
 *   },
 *   "signals": {
 *     "died": [ "string", "int" ]
 *   }
 * }
 *
 * valid type strings are: string, int, double, bool, var, object, array, map
 * object is a qbackend object; it will contain the object structure.
 * var can hold any of the other types
 */

/* Object structure:
 *
 * {
 *   "_qbackend_": "object",
 *   "identifier": "123",
 *   // This is a full type definition object for types that have not been previously defined
 *   "type": "Person",
 *   "data": {
 *     "fullName": "Abazza Bipedal",
 *     "id": 6
 *   }
 * }
 *
 * These are tagged with _qbackend_ to allow them to be identified as values in data,
 * even if the type is not strict.
 *
 * Unless otherwise noted, "data" is comprehensive and any property not included gets a default value.
 */

// XXX error handling
QMetaObject *metaObjectFromType(const QJsonObject &type, const QMetaObject *superClass)
{
    QMetaObjectBuilder b;
    b.setClassName(type.value("name").toString().toUtf8());
    if (superClass)
        b.setSuperClass(superClass);

    b.addProperty("_qb_identifier", "QString").setConstant(true);

    qCDebug(lcObject) << "Building metaobject for type:" << type;

    QJsonObject properties = type.value("properties").toObject();
    for (auto it = properties.constBegin(); it != properties.constEnd(); it++) {
        qCDebug(lcObject) << " -- property:" << it.key() << it.value().toString();
        auto p = b.addProperty(it.key().toUtf8(), qtTypesFromType(it.value().toString()).first.toUtf8());
        // Properties with a matching set* method are marked as writable below
        p.setWritable(false);
    }

    QJsonObject signalsObj = type.value("signals").toObject();
    for (auto it = signalsObj.constBegin(); it != signalsObj.constEnd(); it++) {
        QString signature = it.key() + "(";
        QList<QByteArray> paramNames;
        QJsonArray params = it.value().toArray();
        for (const QJsonValue &p : params) {
            auto pv = p.toString().split(" ");
            signature += qtTypesFromType(pv[0]).first + ",";
            paramNames.append(pv[1].toUtf8());
        }
        if (signature.endsWith(",")) {
            signature.chop(1);
        }
        signature += ")";
        QMetaMethodBuilder method = b.addSignal(signature.toUtf8());
        method.setParameterNames(paramNames);
        qCDebug(lcObject) << " -- signal:" << signature << method.index();

        int c = it.key().lastIndexOf("Changed");
        if (c >= 0) {
            int propIndex = b.indexOfProperty(it.key().mid(0, c).toUtf8());
            if (propIndex >= 0) {
                b.property(propIndex).setNotifySignal(method);
                qCDebug(lcObject) << " -- -- notifying for property" << propIndex;
            }
        }
    }

    QJsonObject methods = type.value("methods").toObject();
    for (auto it = methods.constBegin(); it != methods.constEnd(); it++) {
        QJsonObject info = it.value().toObject();
        QString name = it.key();
        QString signature = name + "(";
        for (const QJsonValue &type : info.value("args").toArray()) {
            signature += qtTypesFromType(type.toString()).first + ",";
        }
        if (signature.endsWith(",")) {
            signature.chop(1);
        }
        signature += ")";

        if (lcObject().isDebugEnabled()) {
            QStringList rv;
            for (const auto &v : info.value("return").toArray()) {
                rv.append(qtTypesFromType(v.toString()).first);
            }
            if (!rv.isEmpty()) {
                qCDebug(lcObject) << " -- method:" << name << signature << "return:" << rv.join(", ");
            } else {
                qCDebug(lcObject) << " -- method:" << name << signature;
            }
        }

        auto m = b.addMethod(signature.toUtf8());
        // Return promises from all methods, even if there is no explicit return value.
        // They may still return errors and this provides a maybe-useful way to tell
        // when a call has finished.
        m.setReturnType("QJSValue");

        if (name.startsWith("set") && info.value("args").toArray().count() == 1) {
            QString propName = name.mid(3);
            if (!propName.isEmpty()) {
                propName[0] = propName[0].toLower();
            }
            int propIndex = b.indexOfProperty(propName.toUtf8());
            if (propIndex >= 0) {
                b.property(propIndex).setWritable(true);
                qCDebug(lcObject) << " -- -- writing property" << propIndex;
            }
        }
    }

    return b.toMetaObject();
}

