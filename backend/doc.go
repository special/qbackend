// qbackend seamlessly bridges a backend Go application with QtQuick/QML for user interfaces.
//
// This package bridges Go and QML for Go applications to implement QML frontends with nearly-seamless sharing
// of objects and types. It aims to be normal and intuitive from either language, with as little API or
// specialized code as possible.
//
// It also allows for out-of-process UI. All frontend/backend communication is socket-based; the Go application
// does not use cgo or any native code. For all-in-one applications, the backend/qmlscene package provides a
// simple wrapper to execute QML within the Go process.
//
// This package includes all of the backend API. In QML, the corresponding Crimson.QBackend plugin provides
// the objects and types exposed from the backend.
//
// Objects
//
// In the middle of everything is QObject. When QObject is embedded in a struct, that type is "a QObject" and
// will be a fully functional Qt object in the frontend. The exported fields become QML properties,
// exported methods are callable functions, and func fields create Qt signals. Properties and parameters can
// contain other QObjects, structs (as JS objects), maps, arrays, and any encodable type. QObject also embeds
// a few useful methods for the backend implementation, such as signalling property changes.
//
//  // Go
//  type Demo struct {
//      qbackend.QObject
//      Steps []*Demo
//  }
//  func (d *Demo) Run() {
//      ...
//  }
//
//
//  // QML
//  property var demo: Backend.topDemo
//  property int numSteps: demo.steps.length
//  onClicked: { demo.run(); demo.steps = [] }
//
// QObjects are used by passing them (by pointer) to the frontend in properties, signals, and return values of
// other objects. They do not need to be initialized explicitly, and they will be garbage collected normally once
// there are no remaining references to the object from Go or QML. Generally, there is no need to treat them
// differently from any other type.
//
// A singleton "root object" is always available within QML as Backend, which makes a useful starting point
// for objects and API.
//
// Data Models
//
// For large, complex, or dynamic data used in QML views, Model provides a QAbstractListModel equivalent API.
// An object which embeds Model, implements the ModelDataSource interface, and calls Model's methods for changes
// to data is usable as a model anywhere in QML.
//
// Instantiable Types
//
// The objectss referenced so far are all created from the Go backend and given to the QML frontend. QML could
// call a function on an existing object to get a new object, but couldn't create anything declaratively. For
// this, we have instantiable types.
//
// Any QObject type registered during startup with Connection.RegisterType becomes instantiable. These are real
// QML types and can be created and used declaratively like any other QML type:
//
//  // Go
//  type Demo struct {
//      qbackend.QObject
//      Value int
//  }
//
//  qb.RegisterType("Demo", &Demo{})
//
//  // QML
//  import Crimson.QBackend 1.0
//
//  Demo {
//      value: 123
//      onValueChanged: { ... }
//  }
//
// Optional interface methods allow the QObject to initialize values and act when construction is completed or
// after QML destruction.
//
// Connection
//
// Connection handles communication with the frontend and manages objects. It's used during during startup but
// otherwise isn't usually important.
//
// Connection is created with a socket for communication with the frontend. Its documentation describes the
// sockets and corresponding behavior of the QML plugin in more detail. In many cases, wrappers like
// backend/qmlscene can be used to avoid dealing with sockets.
//
// Most importantly, the RootObject must be assigned on the connection. This can be any QObject instance of your
// choice. The root object is always available as the Backend singleton in QML. Instantiable types must also be
// registered to the Connection before continuing; they cannot be added once the connection has started.
//
// Finally, the connection is started by calling Run() or (in a loop) Process(). Be aware that any members of
// any initialized QObjects can be accessed during calls to Run, Process, or calls by the application to some
// methods of this package. RunLockable() provides a sync.Locker for exclusive execution with Process(). See
// those methods for details on avoiding concurrency issues.
//
// Executing QML
//
// The choice of how to manage executing the backend and QML client is up to the application. They can be
// separate processes or a single Go process, they can execute together or rely on a daemon, and the client
// or backend could be executed first. qbackend could use more convenient tools for managing this process.
//
// For applications that want to simply run as a Go binary and execute QML in-process, the backend/qmlscene
// package provides a convenient wrapper for qbackend and https://github.com/special/qgoscene. This makes it
// possible to set up an application with only a few lines of code.
package qbackend
