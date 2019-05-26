package main

import (
	qbackend "github.com/CrimsonAS/qbackend/backend"
	"github.com/CrimsonAS/qbackend/backend/qmlscene"
)

type World struct {
	qbackend.QObject
	Message string
	Color   string
}

var qml = `
import QtQuick 2.0
import QtQuick.Window 2.0
import Crimson.QBackend 1.0

Window {
	width: 400
	height: 400
	visible: true

	color: World.color

	Text {
		anchors.centerIn: parent
		color: "white"
		font.pixelSize: 24

		text: World.message
	}
}
`

func main() {
	world := &World{
		Message: "Hello World!",
		Color:   "pink",
	}
	qmlscene.Connection.RegisterSingleton("World", world)
	qmlscene.RunQML(qml)
}
