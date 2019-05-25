package qbackend

import (
	"encoding"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"

	uuid "github.com/satori/go.uuid"
)

const (
	objectRefGracePeriod = 5 * time.Second
)

// Add names of any functions in QObject to the blacklist in type.go

// The QObject interface is embedded in a struct to make that object appear
// as a fully interactive object on the QML frontend. These objects are
// equivalent to a Qt QObject with full support for properties, methods,
// and signals.
//
//  type Thing struct {
//      backend.QObject
//
//      Strings []string
//      Signal func(int) `qbackend:"value"`
//  }
//
//  func (t *Thing) Join(otherThing *Thing) (string, error) {
//      if otherThing == nil {
//          return "", errors.New("missing other Thing")
//      }
//      allStrings := append(t.Strings, otherThing.Strings...)
//      return strings.Join(allStrings, " "), nil
//  }
//
// Properties
//
// Exported fields are properties of the object. To match QML's syntax,
// the first letter of the property name is always made lowercase. Properties
// can be renamed using the `json:"xxx"` field tag. Fields tagged with
// `qbackend:"-"` or `json:"-"` are ignored.
//
// Properties are read-only by default. If a method named "setProp" exists
// and takes one parameter of the correct type, the property "prop" will be
// writable in QML by automatically calling that set method.
//
// Properties have change signals (e.g. "propChanged") automatically. When the
// value of a field changes, call QObject.Changed() with the property name to
// update the value and emit the change signal.
//
// Signals
//
// Signals are defined by exported fields with a func type and a tag with the
// names of its parameters:
//  ThingHappened func(string, string) `qbackend:"what,how"`
// As usual, the first letter of the signal name is lowercase within QML. The
// parameters must be explicitly named; these are the names of variables within
// a QML signal handler. Signals are emitted asynchronously.
//
// During QObject initialization (see below), signal fields are assigned a
// function to emit the signal. After initialization, signals can simply be
// called like methods. Take care when emitting signals from objects that may not
// have been used yet, because the signals may be nil. Custom functions can be
// assigned to the field instead; they will not be replaced during initialization,
// and QObject.Emit() can be used to emit the signal directly.
//
// Methods
//
// Exported methods of the struct can be called as methods on the object.
// To match QML syntax, the first letter of the method name will be lowercase.
// Any serializable (see below) types can be used in parameters and return
// values, including other QObjects.
//
// Calls to Go methods from QML are asynchronous. In QML, all Go backend
// methods return a javascript Promise object. That promise is resolved with
// any return values from the backend or rejected in case of errors. There is
// no way to call methods synchronously.
//
// If the Go method returns multiple values, they are passed as an array when
// the Promise is resolved. If the last (or only) return type is `error` and
// is not nil, the Promise is rejected with that error. Nil errors are not
// included in the return values.
//
// Using the Thing example above from QML:
//
//  Thing {
//      id: obj
//      strings: [ "one", "two" ]
//  }
//  Thing {
//      id: obj2
//      strings: [ "three" ]
//  }
//
//  onClicked: {
//      obj.join(obj2).then(
//          result => {
//              console.log("joined string:", result)
//          },
//          error => {
//              console.warn("join failed:", error)
//          }
//      )
//  }
//
// Serializable Types
//
// Properties and parameters can contain any type serializable as JSON, pointers
// to any QObject type, and any of these types within interfaces, structs, maps,
// slices, and arrays. These are mapped as expected to QML and Javascript types,
// with non-QObject structs as static JS objects. QObjects are mapped to the same
// object instance.
//
// As an implementation detail, serialization uses MarshalJSON for all types other
// than QObjects. QObject implements MarshalJSON to return a light reference to
// the object without any values; serialization is not recursive through QObjects.
// Serialization of the properties of a QObject happens internally. These details
// may change.
//
// Initialization & QObject Methods
//
// QObjects usually don't need explicit initialization. When a QObject is encountered
// in the properties or parameters of another object, it's initialized automatically.
// Initialization assigns the embedded QObject interface, a unique object ID, and sets
// handlers on any nil signal fields. Objects can be initialized immediately with
// Connection.InitObject or Connection.InitObjectId.
//
// The embedded QObject is initially nil, meaning that calls to any of QObject's methods
// will panic. Any unassigned signals will also be nil until the QObject is initialized.
// Take care to check before calling these methods if the object might not have been
// used.
//
// Garbage Collection
//
// QObject types are garbage collected the same as any other type in Go. Once there
// are no references to an object from QML or within the properties of another
// referenced QObject, pointers within qbackend will be released and normal garbage
// collection takes place. There is no need for to handle these differently.
//
// Specifically, the frontend keeps a reference on any object it does or could use,
// so objects can never disappear from under it. During serialization objects keep
// count of references in the properties of other objects, which tracks objects that
// are available to the frontend which it may have not requested yet. If both of
// these are unreferenced, a grace period of a few seconds handles object references
// that may be "in flight" as parameters and debounces.
//
// At the end of that period, there's no valid way for the object to be used
// without first appearing in the serialization of other properties or parameters.
// The unreferenced object is "deactivated", which removes any pointers held by
// qbackend to allow garbage collection, but does not clear the object's unique ID.
//
// If a deactivated object is used again, the object initialization scan reactivates
// it under the same ID and it can be used as if nothing had changed.
//
// Instantiable Types
//
// QObject types registered through Connection.RegisterType() can be created from QML
// declaratively, like any other native type. See that method and the package
// documentation for details.
type QObject struct {
	c        *Connection
	id       string
	ref      bool
	inactive bool

	object   AnyQObject
	typeInfo *typeInfo

	// Number of other objects that have a marshaled reference to this one
	refCount int
	// object id -> count for references to other objects in our properties
	refChildren map[string]int
	// Keep object alive until refGraceTime
	refGraceTime time.Time
}

// AnyQObject is an interface to receive any type usable as a QObject
type AnyQObject interface {
	qObject() *QObject
}

func (q *QObject) qObject() *QObject {
	return q
}

// If a QObject type implements QObjectHasInit, the InitObject function will
// be called immediately after QObject is initialized. This can be used to
// initialize fields automatically at the right time, or even as a form of
// constructor.
type QObjectHasInit interface {
	InitObject()
}

// When instantiable QObjects are created from QML, these methods will be
// called on construction (after all initial properties are set) and
// destruction respectively if they are implemented. It is not necessary
// to implement both methods.
//
// These methods are never called for objects that aren't created from QML.
type QObjectHasStatus interface {
	ComponentComplete()
	ComponentDestruction()
}

// asQObject returns the *QObject for obj, if any, and a boolean indicating if
// obj implements QObject at all.
func asQObject(obj interface{}) (*QObject, bool) {
	if q, ok := obj.(AnyQObject); ok {
		return q.qObject(), true
	} else {
		return nil, false
	}
}

func initObject(object AnyQObject, c *Connection) (*QObject, error) {
	u, _ := uuid.NewV4()
	return initObjectId(object, c, u.String())
}

// XXX split up registration and conenction a bit so reg can happen from anything?
func initObjectId(object AnyQObject, c *Connection, id string) (*QObject, error) {
	var newObject bool
	q := object.qObject()

	if q.id == "" {
		newObject = true
		*q = QObject{
			c:           c,
			id:          id,
			object:      object,
			refChildren: make(map[string]int),
		}

		value := reflect.Indirect(reflect.ValueOf(object))
		if ti, err := parseType(value.Type()); err != nil {
			return nil, err
		} else {
			q.typeInfo = ti
		}

		q.initSignals()
	} else {
		if !q.inactive {
			// Active object, nothing needs to happen here
			return q, nil
		}
		// Reactivating object (after collectObjects)
		q.inactive = false
	}

	// Set grace period to stop the object from being removed prematurely
	q.refsChanged()

	// Register with connection
	if c != nil {
		c.addObject(q)
	}

	// Call InitObject for new objects if implemented
	if newObject {
		if io, ok := object.(QObjectHasInit); ok {
			io.InitObject()
		}
	}

	return q, nil
}

func (q *QObject) initSignals() {
	v := reflect.ValueOf(q.object).Elem()

	for i := 0; i < v.NumField(); i++ {
		field := v.Field(i)
		if typeShouldIgnoreField(v.Type().Field(i)) || field.Type().Kind() != reflect.Func || !field.IsNil() {
			continue
		}

		name := typeFieldName(v.Type().Field(i))
		if _, isSignal := q.typeInfo.Signals[name]; !isSignal {
			continue
		}

		// Build a function to assign as the signal
		f := reflect.MakeFunc(field.Type(), func(args []reflect.Value) []reflect.Value {
			q.emitReflected(name, args)
			return nil
		})
		field.Set(f)
	}

	return
}

// Call after changing o.refCount or o.Ref, or when the grace period should reset
func (o *QObject) refsChanged() {
	if !o.ref && o.refCount < 1 {
		o.refGraceTime = time.Now().Add(objectRefGracePeriod)
	}
}

// XXX Are these functions actually wanted?

// Connection returns the connection associated with this object.
func (o *QObject) Connection() *Connection {
	return o.c
}

// Identifier is unique for each object. Objects can be found by their
// identifier from the Connection. The identifier is randomly assigned,
// unless it was initialized explicitly with Connection.InitObjectId.
func (o *QObject) Identifier() string {
	return o.id
}

// Referenced returns true when there is a client-side reference to
// this object. When false, all signals are ignored and the object
// will not be encoded.
func (o *QObject) Referenced() bool {
	return o.ref
}

// Invoke calls the named method of the object, converting or
// unmarshaling parameters as necessary. An error is returned if the
// method cannot be invoked.
//
// The method's output is returned as an []interface{}. If the last or
// only return type is exactly `error`, it will be removed from the
// values and returned as an error from invoke(). This allows go-style
// errors to be seen as errors by the client without manually checking
// return values.
func (o *QObject) invoke(methodName string, inArgs ...interface{}) ([]interface{}, error) {
	if _, exists := o.typeInfo.Methods[methodName]; !exists {
		return nil, errors.New("method does not exist")
	}

	// Reflect to find a method named methodName on object
	dataValue := reflect.ValueOf(o.object)
	method := typeMethodValueByName(dataValue, methodName)
	if !method.IsValid() {
		return nil, errors.New("method does not exist")
	}
	methodType := method.Type()

	// Build list of arguments
	callArgs := make([]reflect.Value, methodType.NumIn())

	if len(inArgs) != methodType.NumIn() {
		return nil, fmt.Errorf("wrong number of arguments for %s; expected %d, provided %d",
			methodName, methodType.NumIn(), len(inArgs))
	}

	umType := reflect.TypeOf((*encoding.TextUnmarshaler)(nil)).Elem()
	for i, inArg := range inArgs {
		argType := methodType.In(i)
		inArgValue := reflect.ValueOf(inArg)
		var callArg reflect.Value

		// Replace references to QObjects with the objects themselves
		if inArgValue.Kind() == reflect.Map && inArgValue.Type().Key().Kind() == reflect.String {
			objV := inArgValue.MapIndex(reflect.ValueOf("_qbackend_"))
			if objV.Kind() == reflect.Interface {
				objV = objV.Elem()
			}
			if objV.Kind() != reflect.String || objV.String() != "object" {
				return nil, fmt.Errorf("qobject argument %d is malformed; object tag is incorrect", i)
			}
			objV = inArgValue.MapIndex(reflect.ValueOf("identifier"))
			if objV.Kind() == reflect.Interface {
				objV = objV.Elem()
			}
			if objV.Kind() != reflect.String {
				return nil, fmt.Errorf("qobject argument %d is malformed; invalid identifier %v", i, objV)
			}

			// Will be nil if the object does not exist
			// Replace the inArgValue so the logic below can handle type matching and conversion
			inArgValue = reflect.ValueOf(o.c.Object(objV.String()))
		}

		// Match types, converting or unmarshaling if possible
		if inArgValue.Kind() == reflect.Invalid {
			// Zero value, argument is nil
			callArg = reflect.Zero(argType)
		} else if inArgValue.Type() == argType {
			// Types match
			callArg = inArgValue
		} else if inArgValue.Type().ConvertibleTo(argType) {
			// Convert type directly
			callArg = inArgValue.Convert(argType)
		} else if inArgValue.Kind() == reflect.String {
			// Attempt to unmarshal via TextUnmarshaler, directly or by pointer
			var umArg encoding.TextUnmarshaler
			if argType.Implements(umType) {
				callArg = reflect.Zero(argType)
				umArg = callArg.Interface().(encoding.TextUnmarshaler)
			} else if argTypePtr := reflect.PtrTo(argType); argTypePtr.Implements(umType) {
				callArg = reflect.New(argType)
				umArg = callArg.Interface().(encoding.TextUnmarshaler)
				callArg = callArg.Elem()
			}

			if umArg != nil {
				err := umArg.UnmarshalText([]byte(inArg.(string)))
				if err != nil {
					return nil, fmt.Errorf("wrong type for argument %d to %s; expected %s, unmarshal failed: %s",
						i, methodName, argType.String(), err)
				}
			}
		}

		if callArg.IsValid() {
			callArgs[i] = callArg
		} else {
			return nil, fmt.Errorf("wrong type for argument %d to %s; expected %s, provided %s",
				i, methodName, argType.String(), inArgValue.Type().String())
		}
	}

	// Call the method
	returnValues := method.Call(callArgs)

	var err error
	if len(returnValues) > 0 {
		last := len(returnValues) - 1
		if methodType.Out(last) == errorType {
			err, _ = returnValues[last].Interface().(error)
			returnValues = returnValues[:last]
		}
	}

	re := make([]interface{}, len(returnValues))
	for i, v := range returnValues {
		re[i] = v.Interface()
		// The return value could contain a QObject that hasn't been initialized yet,
		// so scan for objects on each value
		o.initObjectsUnder(v)
	}
	return re, err
}

// Emit emits the named signal asynchronously. The signal must be
// defined within the object and parameters must match exactly.
func (o *QObject) Emit(signal string, args ...interface{}) {
	if !o.ref {
		return
	}

	// These arguments go through a plain MarshalJSON from the connection, since they
	// are not being sent as part of an object. The scan to initialize QObjects in
	// this tree needs to happen here.
	if _, err := o.initObjectsUnder(reflect.ValueOf(args)); err != nil {
		// XXX report error
		return
	}

	o.c.sendEmit(o, signal, args)
}

func (o *QObject) emitReflected(signal string, args []reflect.Value) {
	if !o.ref {
		return
	}
	unwrappedArgs := make([]interface{}, 0, len(args))
	for _, a := range args {
		unwrappedArgs = append(unwrappedArgs, a.Interface())
	}
	o.Emit(signal, unwrappedArgs...)
}

// Changed updates the value of a property on the client, and sends
// the changed signal. Changed should be used instead of emitting the
// signal directly; it also handles value updates.
func (o *QObject) Changed(property string) {
	// Currently, all property updates are full resets, and the client will
	// emit changed signals for them. That will hopefully change
	o.ResetProperties()
}

// ResetProperties is effectively identical to emitting the Changed
// signal for all properties of the object.
func (o *QObject) ResetProperties() {
	if !o.ref {
		return
	}
	o.c.sendUpdate(o)
}

// Unfortunately, even though this method is embedded onto the object type, it can't
// be used to marshal the object type. The QObject field is not explicitly initialized;
// it's meant to initialize automatically when an object is encountered. That isn't
// possible to do when this MarshalJSON method is called, even if it were embedded as
// a struct instead of an interface.
//
// Even if this object were guaranteed to have been initialized, QObjects do not
// marshal recursively, and there would be no way to prevent this within MarshalJSON.
//
// Instead, marshalObject handles the correct marshaling of values for object types,
// and this function returns the typeinfo that is appropriate when an object is
// referenced from another object.
func (o *QObject) MarshalJSON() ([]byte, error) {
	var desc interface{}

	// If the client has previously acknowledged an object with this type, there is
	// no need to send the full type structure again; it will be looked up based on
	// typeName.
	if o.c.typeIsAcknowledged(o.typeInfo) {
		desc = struct {
			Name    string `json:"name"`
			Omitted bool   `json:"omitted"`
		}{o.typeInfo.Name, true}
	} else {
		desc = o.typeInfo
	}

	obj := struct {
		Tag        string      `json:"_qbackend_"`
		Identifier string      `json:"identifier"`
		Type       interface{} `json:"type"`
	}{
		"object",
		o.id,
		desc,
	}

	// Marshaling typeinfo for an object resets the refcounting grace period.
	// This ensures that the client has enough time to reference an object from
	// e.g. a signal parameter before it could be cleaned up.
	o.refsChanged()

	return json.Marshal(obj)
}

// As noted above, MarshalJSON can't correctly capture and initialize trees containing
// a QObject. marshalObject scans the struct to initialize QObjects, then returns a
// map that correctly represents the properties of this object. That map can be passed
// (in-)directly to json.Marshal. Specific differences from JSON marshal are:
//
//   - Fields are filtered and renamed in the same manner as properties in typeinfo
//   - Other json tag options on fields are ignored, including omitempty
//   - Signal fields are ignored; these would break MarshalJSON
//   - Any QObject struct encountered is initialized if necessary
//   - QObject structs do not marshal recursively; they only provide typeinfo
//   - Array, slice, non-QObject struct, and map fields are scanned for QObjects and
//     marshal appropriately
//
// Non-QObject fields will be marshaled normally with json.Marshal.
func (o *QObject) marshalObject() (map[string]interface{}, error) {
	data := make(map[string]interface{})

	// Zero out all child ref counts
	for k, _ := range o.refChildren {
		o.refChildren[k] = 0
	}

	value := reflect.Indirect(reflect.ValueOf(o.object))
	for name, index := range o.typeInfo.propertyFieldIndex {
		field := value.FieldByIndex(index)
		if refs, err := o.initObjectsUnder(field); err != nil {
			return nil, err
		} else {
			// Add references from refs
			for _, id := range refs {
				if _, existing := o.refChildren[id]; !existing {
					// Reference to an object that was not referenced before
					if obj := o.c.Object(id); obj != nil {
						impl, _ := asQObject(obj)
						impl.refCount++
						o.refsChanged()
					}
				}
				o.refChildren[id]++
			}

			// Dereference objects that are no longer referenced here
			for k, v := range o.refChildren {
				if v > 0 {
					continue
				}
				delete(o.refChildren, k)
				if obj := o.c.Object(k); obj != nil {
					impl, _ := asQObject(obj)
					impl.refCount--
					o.refsChanged()
				}
			}
		}
		data[name] = field.Interface()
	}

	return data, nil
}

// initObjectsUnder scans a Value for references to any QObject types, and
// initializes these if necessary. This scan is recursive through any types
// other than QObject itself.
//
// This scan also maintains the list of object IDs referenced within this
// object, which is returned here and stored as refChildren.
func (o *QObject) initObjectsUnder(v reflect.Value) ([]string, error) {
	for v.Kind() == reflect.Ptr || v.Kind() == reflect.Interface {
		v = v.Elem()
		if !v.IsValid() {
			// nil pointer/interface
			return nil, nil
		}
	}

	var refs []string

	switch v.Kind() {
	case reflect.Array:
		fallthrough
	case reflect.Slice:
		elemType := v.Type().Elem()
		if !typeCouldContainQObject(elemType) {
			return nil, nil
		}
		for i := 0; i < v.Len(); i++ {
			if elemRefs, err := o.initObjectsUnder(v.Index(i)); err != nil {
				return nil, err
			} else {
				refs = append(refs, elemRefs...)
			}
		}

	case reflect.Map:
		elemType := v.Type().Elem()
		if !typeCouldContainQObject(elemType) {
			return nil, nil
		}
		for _, key := range v.MapKeys() {
			if elemRefs, err := o.initObjectsUnder(v.MapIndex(key)); err != nil {
				return nil, err
			} else {
				refs = append(refs, elemRefs...)
			}
		}

	case reflect.Struct:
		if obj, ok := v.Addr().Interface().(AnyQObject); ok {
			if q, err := initObject(obj, o.c); err == nil {
				// Valid QObject, possibly just initialized. Stop recursion here
				refs = append(refs, q.id)
				return refs, nil
			} else {
				return nil, err
			}
		}

		// Not a QObject
		for i := 0; i < v.NumField(); i++ {
			if typeShouldIgnoreField(v.Type().Field(i)) {
				continue
			}
			field := v.Field(i)
			if typeCouldContainQObject(field.Type()) {
				if elemRefs, err := o.initObjectsUnder(field); err != nil {
					return nil, err
				} else {
					refs = append(refs, elemRefs...)
				}
			}
		}
	}

	return refs, nil
}

func typeCouldContainQObject(t reflect.Type) bool {
	for {
		switch t.Kind() {
		case reflect.Array:
			fallthrough
		case reflect.Slice:
			fallthrough
		case reflect.Map:
			fallthrough
		case reflect.Ptr:
			t = t.Elem()
			continue

		case reflect.Struct:
			fallthrough
		case reflect.Interface:
			return true

		default:
			return false
		}
	}
}
