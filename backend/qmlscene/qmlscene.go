// qmlscene combines qbackend with one-line execution of QML applications.
//
// qmlscene combines https://github.com/special/qgoscene with qbackend, for a convenient
// way to build and run qbackend applications. qgoscene is a very simple API to run QML
// in a Go process. qgoscene links to Qt directly.
//
// In simple cases, an application can execute with:
//
//	qmlscene.Connection.RegisterType(...)
//	qmlscene.RunFile("main.qml")
package qmlscene

import (
	"fmt"
	"os"

	qbackend "github.com/CrimsonAS/qbackend/backend"
	"github.com/special/qgoscene"
)

// Connection is the instance of qbackend.Connection used by the scene
//
// Connection is created automatically on init and will never be nil.
var Connection *qbackend.Connection

// Scene is the scene that has been created, if any
//
// Do not call Scene.Exec directly unless Connection has already been
// started. Call Run instead.
var Scene *qgoscene.Scene

var rB, wB, rF, wF *os.File

func init() {
	rB, wB, _ = os.Pipe()
	rF, wF, _ = os.Pipe()
	Connection = qbackend.NewConnectionSplit(rF, wB)
}

func sceneArgs() []string {
	return append(os.Args, []string{"-qbackend", fmt.Sprintf("fd:%d,%d", rB.Fd(), wF.Fd())}...)
}

// NewScene creates a new scene from a QML file and returns it.
//
// The new scene is also available as qmlscene.Scene and can be used to change
// import paths and context properties.
//
// This function will panic if a scene has already been created.
func NewScene(qmlFile string) *qgoscene.Scene {
	if Scene != nil {
		panic("qmlscene does not support multiple scenes")
	}
	Scene = qgoscene.NewScene(qmlFile, sceneArgs())
	return Scene
}

// NewSceneFromData creates a new scene from a string of QML and returns it.
//
// The new scene is also available as qmlscene.Scene and can be used to change
// import paths and context properties.
//
// This function will panic if a scene has already been created.
func NewSceneFromData(qml string) *qgoscene.Scene {
	if Scene != nil {
		panic("qmlscene does not support multiple scenes")
	}
	Scene = qgoscene.NewSceneData(qml, sceneArgs())
	return Scene
}

// Run starts the QML application and connection if necessary.
//
// A scene must have already been created with NewScene or NewSceneFromData.
// If Connection has not been started yet, it will run in a goroutine. Start
// the connection manually before calling Run for more control. Run does not
// return. The process will exit when the Qt application exits.
func Run() {
	if Scene == nil {
		panic("qmlscene executed without a scene loaded")
	}
	if !Connection.Started() {
		go Connection.Run()
	}
	os.Exit(Scene.Exec())
}

// RunFile is equivalent to NewScene followed by Run
func RunFile(qmlFile string) {
	NewScene(qmlFile)
	Run()
}

// RunQML is equivalent to NewSceneFromData followed by Run
func RunQML(qml string) {
	NewSceneFromData(qml)
	Run()
}
