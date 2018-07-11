# QBackend Architecture & Implementation

QBackend exposes Go structs as objects in QML, with methods, fields as properties, and function
fields as signals. These don't require any declaration, boilerplate, or initialization. Generally,
turning a struct into a full exported object requires one line: `qbackend.QObject`. These objects
are still garbage collected normally, once nothing in the frontend has a reference.

In QML, the backend is initialized from `import QBackend 1.0`. Backend types registered at runtime
as instantiable are real QML types under their real names. Backend objects are real QObjects. They
can be stored, passed as parameters, and have methods, properties, and signals as you'd expect.
Types that implement an API become QAbstractListModels in QML, directly usable with views.

QBackend does everything over a local socket; there is no use of cgo or cross-language bindings.
A Go backend and QML frontend don't need to run in the same process. The go package does not link
to Qt or any native dependencies.

QBackend is meant to get out of the way while you write real applications.

## Overview

This document aims to explain the design and implementation in detail. To write applications, the
[Go API](XXX) and [QML API](XXX) are better resources.

At a very high level, QBackend uses Go reflection and JSON marshaling to pass types and objects to
the client, which converts these into QMetaObjects with the same API. About half of this document
is describing that process.

The remainder is about "seamless API": everything should behave the way you normally expect it to
behave. Everywhere possible, you shouldn't need to do things in a special way or use QBackend
directly. Objects are objects, types are types, models are models, on both sides. Naturally, that
can make QBackend's implementation horrendously complicated.

The _connection_ manages a socket and tracks known objects. _Object_ refers to an exported instance
of a Go struct (with methods/properties/signals). On both sides, these are represented by the
respective QObject types. _Type_ or _typeinfo_  is a JSON description of an object type, generated
by Go reflection and interpreted for a QMetaObject. The _root object_ is a user-provided singleton
object that is always known to the client, so it is a useful starting point for passing objects.

### Client (QML)

Client API is provided by the QBackend plugin. It can be imported in two ways: as a singleton with
`import QBackend 1.0`, or in a more manual connection mode with `import QBackend.Connection 1.0`.

The `QBackend.Connection` import is less common. It has `BackendConnection` and `BackendProcess`
types, which set up connection(s) or start a backend process. This requires actual API. Seamless
instantiable types aren't available with this method.

The normal `QBackend` import does everything for you. It will determine how to make a connection
(see [Transport](XXX)) and handle this invisibly. If the backend has [instantiable types](XXX),
these are registered as QML types during import, with their actual names and types. The only other
API is a singleton `Backend`, which is the root object from the backend.

Most of the QML API is done by dynamically building a QMetaObject and handling the appropriate
metacalls. Instantiable types are more complex and require a templated placeholder type as well.

In a few places, it's necessary to block for messages from the backend. That should be minimized,
but it otherwise wouldn't be possible to have code like `obj1.obj2.prop`, instantiable types, or
avoid putting null checks on absolutely everything in your QML.

Types are their own complicated topic, but briefly: in the metaobject, primitive types and strings
are themselves, backend objects are QObject\*, and all other types (notably array/map) are QJSValue.

Garbage collection is a more complicated topic, explained below. Objects are given javascript
ownership as they're passed to QML. As these are collected by JS, a reference is released on the
backend. Once a backend object isn't referenced by the client or any properties, it's removed by
the backend, where it can be found by the Go GC or re-added as it reappears.

The [QML API Reference](XXX) has more detail on using QBackend in QML applications.

### Backend (Go)

- Connection, message loop,  threadsafety
- Root object
- QObject embedding & initialization
- Properties, methods, and signals
- Refer to api doc

On the backend, a connection is created, given a _root object_, and run in a goroutine. The root
object is a singleton, always available to the client as `Backend`.

On the backend, any struct embedding `qbackend.QObject` becomes a full object

## Connections

- Simple text/JSON protocol over sockets
- Sometimes used entirely in-process, sometimes multi-process
- Only intended for local use; latency sensitive

### Transport

- Autoconfig
- In-process with pipes
- Unix socket
- Message framing

### Protocol

- "command" field
- On connection, backend sends VERSION, CREATABLE_TYPES, ROOT in that order
- Client is sometimes synchronous; e.g. initialization and blocking until an OBJECT_RESET

##### Backend: VERSION

##### Backend: CREATABLE_TYPES

##### Backend: ROOT

##### Client: OBJECT_REF & OBJECT_DEREF

##### Backend: OBJECT_RESET

##### Client: INVOKE

##### Backend: EMIT

### Object Management

- Connection keeps map of identifiers to actual object instances
- Object identifiers are unique for the lifetime of the connection

## Objects

- Instances of Go structs on the backend available as QML objects for the client
- Supporting properties, methods, signals as normally as possible
- Nearly transparent

### Identifiers

- random, arbitrary
- unique for lifetime of connection
- root

### Properties

- mirroring fields of the Go struct; excluding signals
- lowercasing
- Go types mapped to JSON-like types: bool, int, double, string, object, var
- Marshaled similar to Go JSON, but not exactly the same
- Object values are special references to another backend object; see Object References
- Properties have change signals named propertyChanged, even if not explicitly defined
- Backend code must explicitly notify changes to properties to update on client
- Writable when there is a method 'setName'

### Methods

- mirroring exported methods on the Go struct
- called asynchronously, no return values
- parameters, including backend objects, work as you'd expect
- lowercasing

### Signals

- Declared in Go as func() fields
- When object is initialized, assigned a handler to emit the signal. Can do this explicitly instead.
- Parameters work like they do for methods or properties
- Must explicitly specify parameter names, which are used in QML
- Real signals on the Qt object, everything as usual

### Typeinfo

- JSON description of properties/methods/signals for a type
- Created on backend by reflection, used by client to build the metaobject
- Simplified data types, roughly corresponding to JSON types + backend object

##### JSON Format

### Object References

- Reference to a backend object in JSON
- Transformed on both sides during (de)marshal, never visible to the application
- Sufficient type information to create an object, but no property values

##### JSON Format

### Lifetime & Garbage Collection

- Somewhat complex system to allow the QML and Go garbage collectors to work
- Developers shouldn't have to worry about it. Objects garbage collect as you'd
  expect on both sides, once they're not possible to reference from the client.

##### Client (Qt)

- Objects have JS ownership; garbage collected when no JS reference remains
- Backend is notified for instantiation/destruction of objects (OBJECT_REF & OBJECT_DEREF)
- It is safe to keep the object elsewhere in QML; as long as there's a reference, it still exists
- The only valid ways to get objects in QML is through properties or signal parameters

##### Backend (Go)

- Backend keeps a map of identifier -> object
- Objects are internally reference counted
- Anywhere an object appears in the properties of another object is a reference
- Client's OBJECT_REF is also a reference
- Grace period for signal parameters and race conditions
- It's not possible for the client to have a valid reference to the object anymore
- Remove from identifier map; object can be GC'd by Go if it's not kept elsewhere
- If the object still exists, it will be added back when it's next marshalled, so
  this case is transparent.

## Instantiable Types

- Object types exported from Go, available as real types in QML
- Must be registered on backend before connecting
- Limited to 10
- Only available with the singleton-style import (currently)
- Models too

## Models

TBD
