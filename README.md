# Seamless Go + QML applications
[![GoDoc](https://godoc.org/github.com/CrimsonAS/qbackend/backend?status.svg)](https://godoc.org/github.com/CrimsonAS/qbackend/backend)

QBackend connects a Go application to a QtQuick UI with transparent remote objects in seamless and natural API.

#### Go Backend
* Go structs become fully featured objects in QML
* Objects have properties (exported fields), methods, and signals (exported func fields) from the struct
* No object/type initialization or boilerplate needed; just embed `qbackend.QObject`
* Garbage collection works as usual once an object isn't referenced from Go or QML
* Fields and parameters can be bool, ints, floats, or strings and nested arrays, maps, interfaces, or structs.
* Structs with QObject are passed to or from QML by reference

#### QML User Interface
* Backend API is completely transparent; just `import Crimson.QBackend 1.0`
* Types defined in Go can be created declaratively (`YourType { }`) in QML
* Singleton objects created in Go can become QML singletons (`YourType.value`)
* Backend objects implementing a model API can be used as QAbstractItemModel directly

#### Convenience
* The optional `qmlscene` package runs QML in-process for all in one binaries

## Why?

Go is great, but the options for UIs aren't. QtQuick is easy, flexible, and maturing. I enjoy using both of them, and I want to use them together.

[go-qml](https://github.com/go-qml/qml) (and its more recent forks) have existed for years. Its scope is very different, closer to writing Go bindings for QtQuick, and like most bindings the API is somewhere between clunky and broken. I think it's fundamentally the wrong approach, trying to match low level behavior rather than build good high level API. The original package is abandoned and lacked support for things like models or arrays.

Go and Qt both have powerful object reflection, so it's possible to dynamically create types and build essentially native objects. The potential exists for seamless APIs that don't feel like an intrusive hack bridging between languages.

There is interesting potential in multiprocess graphical applications, too. There is no need to rely on cgo for this design.

## Usage

TODO: Documentation ;)

Until then, examples and code comments are all you get.

## How It Works

TODO: Design documentation!

Most of this is not necessary to understand in order to use qbackend. A short, dense introduction:

The connection is a socket and JSON-based RPCish protocol between the backend (Go package) and client (QML plugin). Go structs embed `qbackend.QObject`, effectively similar to inheriting QObject in Qt. These can be reflected to build type definitions, and will be marshaled and sent to the client as-needed. Fields become properties, exported methods are methods, and function type fields are signals. Types can be registered as instantiable or objects as singletons, which are defined as real types and singletons in the QML plugin; these are the entry points for a backend API. When marshaling objects, any struct encountered with `qbackend.QObject` is automatically initialized and assigned a unique id. QObjects marshal as a type and id reference rather than their full data, which is sent once the client references the object. QObject embeds a handful of useful methods, like `Changed` and `ResetProperties` to signal updates. Internal reference counting tracks which objects the client has or could use, and drops unreferenced objects from its list to allow garbage collection. If those objects are used again instead, nothing will have changed. Overall, you can just treat these as any struct and the right things will happen.

From QML, importing the plugin establishes a connection and registers any instantiable types and singletons (uncreateable types do not need any registration). Object types are a JSON description generated from Go reflection, which is translated to a QMetaObject, which effectively _is_ a QObject from QML's point of view. Backend objects are created with an adaptor type using the QMetaObject, which provides all of the metacalls to handle property reads/writes, method calls, and signals. The adaptor object is a reference to the instance from the backend, and will be GC'd when no longer referenced from QML, which allows the backend object to be freed by Go GC. Adaptors are created (and objects referenced) as-needed when an 'object ref' is encountered in data being returned to QML, but initially have a type description with no data. Object data is populated just-in-time when properties of the object are actually used.

Models are just objects that provide a particular set of methods. On the backend, `qbackend.Model` provides an API for this (and is also a QObject). From the client, these are normal objects that also inherit QAbstractListModel.

## Development

TODO: A list of things to do

