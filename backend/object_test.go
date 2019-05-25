package qbackend

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"testing"
)

var dummyConnection *Connection

type BasicStruct struct {
	StringData string
}

type BasicQObject struct {
	QObject

	StringData string
	StructData BasicStruct
	Child      *BasicQObject
	Signal     func()

	initWasCalled bool
}

func (o *BasicQObject) InitObject() {
	o.initWasCalled = true
}

func TestMain(m *testing.M) {
	r1, _ := io.Pipe()
	_, w2 := io.Pipe()
	dummyConnection = NewConnectionSplit(r1, w2)

	os.Exit(m.Run())
}

func TestQObjectInit(t *testing.T) {
	q := &BasicQObject{}

	// These should silently do nothing on an uninitialized object
	q.Emit("signal")
	q.ResetProperties()
	q.Changed("stringData")

	if err := dummyConnection.InitObject(q); err != nil {
		t.Errorf("QObject initialization failed: %s", err)
	}

	if q.QObject.id == "" {
		t.Error("Embedded QObject still blank after initialization")
	}

	if q.Signal == nil {
		t.Error("Signal function not initialized by QObject")
	}

	if !q.initWasCalled {
		t.Error("QObjectHasInit initialization function not called")
	}

	t.Logf("Typeinfo: %v", q.QObject.typeInfo)
}

func TestMarshal(t *testing.T) {
	q := &BasicQObject{
		StringData: "hello world",
		StructData: BasicStruct{"hello struct"},
		Child: &BasicQObject{
			StringData: "hello child",
		},
	}

	if err := dummyConnection.InitObject(q); err != nil {
		t.Errorf("QObject initialization failed: %s", err)
	}

	data, err := q.marshalObject()
	if err != nil {
		t.Errorf("QObject marshal failed: %s", err)
	}
	jsonData, err := json.Marshal(data)
	if err != nil {
		t.Errorf("JSON marshal failed: %s", err)
	}

	t.Logf("Marshaled object: %s", jsonData)
}

type SignalQObject struct {
	QObject
	NoArgs     func()
	NormalArgs func([]int, string) `qbackend:"ints,str"`
	ObjectArgs func(*BasicQObject) `qbackend:"obj"`
}

func TestSignals(t *testing.T) {
	q := &SignalQObject{}

	// Init should assign functions for each signal
	if err := dummyConnection.InitObject(q); err != nil {
		t.Errorf("QObject initialization failed: %s", err)
	}
	if q.NoArgs == nil || q.NormalArgs == nil || q.ObjectArgs == nil {
		t.Errorf("QObject initialization didn't initialize signals: %+v", q)
	}

	ti, _ := json.Marshal(q.QObject.typeInfo)
	t.Logf("Typeinfo: %s", ti)

	// Emit signals
	q.NoArgs()
	q.NormalArgs([]int{1, 2, 3, 4, 5}, "one to five")
	q.ObjectArgs(&BasicQObject{StringData: "i am object argument"})
}

type MethodQObject struct {
	QObject
	Count int
}

func (m *MethodQObject) Increment() {
	m.Count++
}

func (m *MethodQObject) Add(i int) {
	m.Count += i
}

func (m *MethodQObject) Update(obj *BasicQObject) {
	if obj != nil {
		obj.StringData = fmt.Sprintf("Count is %d", m.Count)
	}
}

func TestMethods(t *testing.T) {
	q := &MethodQObject{}

	if err := dummyConnection.InitObject(q); err != nil {
		t.Errorf("QObject initialization failed: %s", err)
	}

	ti, _ := json.Marshal(q.QObject.typeInfo)
	t.Logf("Typeinfo: %s", ti)

	_, err := q.invoke("increment")
	if err != nil || q.Count != 1 {
		t.Errorf("Invoking 'Increment' failed: %v", err)
	}

	_, err = q.invoke("add", 4)
	if err != nil || q.Count != 5 {
		t.Errorf("Invoking 'Add' failed: %v", err)
	}

	strObj := &BasicQObject{}
	if err := dummyConnection.InitObject(strObj); err != nil {
		t.Errorf("Initializing object failed: %v", err)
	}

	// There's generally no reason to refer objects this way from Go, so
	// fake the API a little bit.
	strObjRef := make(map[string]string)
	strObjRef["_qbackend_"] = "object"
	strObjRef["identifier"] = strObj.Identifier()
	if _, err := q.invoke("update", strObjRef); err != nil {
		t.Errorf("Invoking 'Update' failed: %v", err)
	}
	if strObj.StringData != "Count is 5" {
		t.Error("Object passed as parameter was not modified")
	}
}

type NestedQObject struct {
	QObject

	Object    *NestedQObject
	InStruct  struct{ Object *NestedQObject }
	Interface AnyQObject
	SelfRef   *NestedQObject
	Double1   *NestedQObject
	Double2   *NestedQObject
}

func TestQObjectValue(t *testing.T) {
	obj := &NestedQObject{
		Object: &NestedQObject{
			Object: &NestedQObject{},
		},
		InStruct:  struct{ Object *NestedQObject }{&NestedQObject{}},
		Interface: &NestedQObject{},
		Double1:   &NestedQObject{},
	}
	obj.SelfRef = obj
	obj.Double2 = obj.Double1
	nestedObject := obj.Object

	if err := dummyConnection.InitObject(obj); err != nil {
		t.Errorf("QObject initialization failed: %s", err)
		return
	}

	if _, err := obj.marshalObject(); err != nil {
		t.Errorf("QObject marshalling failed: %s", err)
		return
	}

	checkObject := func(name string, child *QObject, refObjCount int, refFieldCount int) {
		if child.id == "" {
			t.Errorf("Field '%s' QObject value was not initialized during marshal", name)
		}
		// refCount is the number of _objects_ referencing, not the number of fields
		if child.refCount != refObjCount {
			t.Errorf("Field '%s' QObject value's refcount is wrong (%d != %d (expected))", name, child.refCount, refObjCount)
		}
		if obj.refChildren[child.id] != refFieldCount {
			t.Errorf("Field '%s' QObject value's refChildren entry is wrong (%d != %d (expected))", name, obj.refChildren[child.id], refFieldCount)
		}
	}

	checkObject("Object", obj.Object.qObject(), 1, 1)
	if obj.Object.Object.id != "" {
		t.Errorf("QObject somehow initialized objects recursively")
	}
	checkObject("InStruct", obj.InStruct.Object.qObject(), 1, 1)
	checkObject("Interface", obj.Interface.qObject(), 1, 1)
	// XXX how should self references work? can objects accidentally keep themselves alive?
	checkObject("SelfRef", obj.SelfRef.qObject(), 1, 1)
	checkObject("Double1", obj.Double1.qObject(), 1, 2)

	obj.Double2 = nil
	obj.Object = nil
	if _, err := obj.marshalObject(); err != nil {
		t.Errorf("QObject marshalling failed: %s", err)
		return
	}

	checkObject("Object", nestedObject.qObject(), 0, 0)
	checkObject("Double1", obj.Double1.qObject(), 1, 1)
}
