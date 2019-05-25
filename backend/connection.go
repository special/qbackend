package qbackend

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"reflect"
	"strconv"
	"time"
)

type Connection struct {
	in           io.ReadCloser
	out          io.WriteCloser
	objects      map[string]*QObject
	instantiable map[string]instantiableType
	knownTypes   map[string]struct{}
	err          error

	started       bool
	processSignal chan struct{}
	queue         chan []byte
}

// NewConnection creates a new connection from an open stream. To use the
// connection, types must be registered, and Run() or Process() must be
// called to start processing data.
func NewConnection(data io.ReadWriteCloser) *Connection {
	return NewConnectionSplit(data, data)
}

// NewSplitConnection is equivalent to Connection, except that it uses spearate
// streams for reading and writing. This is useful for certain kinds of pipe or
// when using stdin and stdout.
func NewConnectionSplit(in io.ReadCloser, out io.WriteCloser) *Connection {
	c := &Connection{
		in:            in,
		out:           out,
		objects:       make(map[string]*QObject),
		instantiable:  make(map[string]instantiableType),
		knownTypes:    make(map[string]struct{}),
		processSignal: make(chan struct{}, 2),
		queue:         make(chan []byte, 128),
	}
	return c
}

type instantiableFactory func() AnyQObject

type instantiableType struct {
	Type    *typeInfo
	Factory instantiableFactory
}

type messageBase struct {
	Command string `json:"command"`
}

func (c *Connection) fatal(fmsg string, p ...interface{}) {
	msg := fmt.Sprintf(fmsg, p...)
	log.Print("qbackend: FATAL: " + msg)
	if c.err == nil {
		c.err = fmt.Errorf(fmsg, p...)
		c.in.Close()
		c.out.Close()
	}
}

func (c *Connection) warn(fmsg string, p ...interface{}) {
	log.Printf("qbackend: WARNING: "+fmsg, p...)
}

func (c *Connection) sendMessage(msg interface{}) {
	buf, err := json.Marshal(msg)
	if err != nil {
		c.fatal("message encoding failed: %s", err)
		return
	}
	fmt.Fprintf(c.out, "%d %s\n", len(buf), buf)
}

// handle() runs in an internal goroutine to read from 'in'. Messages are
// posted to the queue and processSignal is triggered.
func (c *Connection) handle() {
	defer close(c.processSignal)
	defer close(c.queue)

	// VERSION
	c.sendMessage(struct {
		messageBase
		Version int `json:"version"`
	}{messageBase{"VERSION"}, 2})

	// CREATABLE_TYPES
	{
		types := make([]*typeInfo, 0, len(c.instantiable))
		for _, t := range c.instantiable {
			types = append(types, t.Type)
		}

		c.sendMessage(struct {
			messageBase
			Types []*typeInfo `json:"types"`
		}{
			messageBase{"CREATABLE_TYPES"},
			types,
		})
	}

	rd := bufio.NewReader(c.in)
	for c.err == nil {
		sizeStr, err := rd.ReadString(' ')
		if err != nil {
			c.fatal("read error: %s", err)
			return
		} else if len(sizeStr) < 2 {
			c.fatal("read invalid message: invalid size")
			return
		}

		byteCnt, _ := strconv.ParseInt(sizeStr[:len(sizeStr)-1], 10, 32)
		if byteCnt < 1 {
			c.fatal("read invalid message: size too short")
			return
		}

		blob := make([]byte, byteCnt)
		for p := 0; p < len(blob); {
			if n, err := rd.Read(blob[p:]); err != nil {
				c.fatal("read error: %s", err)
				return
			} else {
				p += n
			}
		}

		// Read the final newline
		if nl, err := rd.ReadByte(); err != nil {
			c.fatal("read error: %s", err)
			return
		} else if nl != '\n' {
			c.fatal("read invalid message: expected terminating newline, read %c", nl)
			return
		}

		// Queue and signal
		c.queue <- blob
		c.processSignal <- struct{}{}
	}
}

func (c *Connection) ensureHandler() error {
	if !c.started {
		c.started = true

		if c.err != nil {
			return c.err
		} else {
			go c.handle()
		}
	}

	return nil
}

// Started returns true if the connection has been started by a call to Run() or Process()
func (c *Connection) Started() bool {
	return c.started
}

// Run processes messages until the connection is closed. Be aware that when using Run,
// any data exposed in objects could be accessed by the connection at any time. For
// better control over concurrency, see Process.
//
// Run is equivalent to a loop of Process and ProcessSignal.
func (c *Connection) Run() error {
	c.ensureHandler()
	for {
		if _, open := <-c.processSignal; !open {
			return c.err
		}
		if err := c.Process(); err != nil {
			return err
		}
	}
	return nil
}

// Process handles any pending messages on the connection, but does not block to wait
// for new messages. ProcessSignal signals when there are messages to process.
//
// Application data (objects and their fields) is never accessed except during calls to
// Process() or other qbackend methods. By controlling calls to Process, applications
// can avoid concurrency issues with object data.
//
// Process returns nil when no messages are pending. All errors are fatal for the
// connection.
func (c *Connection) Process() error {
	c.ensureHandler()
	lastCollection := time.Now()

	for {
		var data []byte
		select {
		case data = <-c.queue:
		default:
			return c.err
		}

		var msg map[string]interface{}
		if err := json.Unmarshal(data, &msg); err != nil {
			c.fatal("process invalid message: %s", err)
			// once queue is closed, the error from fatal will be returned
			continue
		}

		identifier := msg["identifier"].(string)
		obj, objExists := c.objects[identifier]
		impl, _ := asQObject(obj)

		switch msg["command"] {
		case "OBJECT_REF":
			if objExists {
				impl.ref = true
				impl.refsChanged()
				// Record that the client has acknowledged an object of this type
				c.knownTypes[impl.typeInfo.Name] = struct{}{}
			} else {
				c.warn("ref of unknown object %s", identifier)
			}

		case "OBJECT_DEREF":
			if objExists {
				impl.ref = false
				impl.refsChanged()
			} else {
				c.warn("deref of unknown object %s", identifier)
			}

		case "OBJECT_QUERY":
			if objExists {
				c.sendUpdate(impl)
			} else {
				c.fatal("query of unknown object %s", identifier)
			}

		case "OBJECT_CREATE":
			if objExists {
				c.fatal("create of duplicate identifier %s", identifier)
				break
			}

			if t, ok := c.instantiable[msg["typeName"].(string)]; !ok {
				c.fatal("create of unknown type %s", msg["typeName"].(string))
				break
			} else {
				obj := t.Factory()
				impl, _ := initObjectId(obj, c, identifier)
				impl.ref = true
			}

		case "INVOKE":
			method := msg["method"].(string)
			if objExists {
				params, ok := msg["parameters"].([]interface{})
				if !ok {
					c.fatal("invoke with invalid parameters of %s on %s", method, identifier)
					break
				}
				returnId, _ := msg["return"].(string)

				re, err := impl.invoke(method, params...)
				if returnId != "" {
					var errString string
					if err != nil {
						errString = err.Error()
					}

					c.sendMessage(struct {
						messageBase
						Identifier string        `json:"identifier"`
						Return     string        `json:"return"`
						Error      string        `json:"error,omitempty"`
						Value      []interface{} `json:"value,omitempty"`
					}{
						messageBase{"INVOKE_RETURN"},
						impl.Identifier(),
						returnId,
						errString,
						re,
					})
				}
			} else {
				c.fatal("invoke of %s on unknown object %s", method, identifier)
			}

		default:
			c.fatal("unknown command %s", msg["command"])
		}

		// Scan references for garbage collection at most every 5 seconds
		if now := time.Now(); now.Sub(lastCollection) >= 5*time.Second {
			c.collectObjects()
			lastCollection = now
		}
	}

	return nil
}

func (c *Connection) ProcessSignal() <-chan struct{} {
	c.ensureHandler()
	return c.processSignal
}

func (c *Connection) addObject(obj *QObject) {
	q := obj.qObject()
	id := q.Identifier()
	if eObj, exists := c.objects[id]; exists {
		if q == eObj {
			return
		} else {
			c.fatal("registered different object with duplicate identifier %s", id)
			return
		}
	}

	c.objects[id] = q
}

// Remove objects that have no property references, are not referenced by
// the client, and have passed their grace period from the map, allowing
// the GC to collect them. Under these conditions, there is no valid way
// for a client to reference the object. If the object is used again, it
// will be re-added under the same ID.
func (c *Connection) collectObjects() {
	for id, obj := range c.objects {
		impl, _ := asQObject(obj)
		if !impl.ref && impl.refCount < 1 && time.Now().After(impl.refGraceTime) {
			delete(c.objects, id)
			impl.inactive = true
		}
	}
}

// Object returns a registered QObject by its identifier
func (c *Connection) Object(name string) AnyQObject {
	return c.objects[name].object
}

// InitObject explicitly initializes a QObject, assigning an identifier and
// setting up signal functions.
//
// It's not necessary to InitObject manually. Objects are automatically
// initialized as they are encountered in properties and parameters.
//
// InitObject can be useful to guarantee that a QObject and its signals are
// non-nil and can be called, even when it may have not yet been sent to the
// client.
func (c *Connection) InitObject(obj AnyQObject) error {
	_, err := initObject(obj, c)
	return err
}

// InitObjectId is equvialent to InitObject, but takes an identifier for the
// object. Nothing is changed if the object has already been initialized.
//
// In some cases, it's useful to look up an object by a known/composed name,
// because holding a reference to that object would prevent garbage collection.
// This is particularly true when writing wrapper types where the object is
// uniquely wrapping another non-QObject type.
func (c *Connection) InitObjectId(obj AnyQObject, id string) error {
	if eobj, exists := c.objects[id]; exists && obj != eobj {
		return errors.New("object id in use")
	}
	_, err := initObjectId(obj, c, id)
	return err
}

func (c *Connection) sendUpdate(impl *QObject) error {
	if !impl.Referenced() {
		return nil
	}

	data, err := impl.marshalObject()
	if err != nil {
		c.warn("marshal of object %s (type %s) failed: %s", impl.id, impl.typeInfo.Name, err)
		return err
	}

	c.sendMessage(struct {
		messageBase
		Identifier string                 `json:"identifier"`
		Data       map[string]interface{} `json:"data"`
	}{
		messageBase{"OBJECT_RESET"},
		impl.Identifier(),
		data,
	})
	return nil
}

func (c *Connection) sendEmit(obj *QObject, method string, data []interface{}) error {
	c.sendMessage(struct {
		messageBase
		Identifier string        `json:"identifier"`
		Method     string        `json:"method"`
		Parameters []interface{} `json:"parameters"`
	}{messageBase{"EMIT"}, obj.Identifier(), method, data})
	return nil
}

// RegisterTypeFactory registers a type to be creatable from QML. Instances of these types
// can be created, assigned properties, and used declaratively like any other QML type.
//
// The factory function is called to create a new instance. The QObject 't' should be a
// pointer to the zero value of the type (&Type{}). This is used only for type definition,
// and its value has no meaning. The factory function must always return this same type.
//
// RegisterType must be called before the connection starts (calling Process or Run).
// There is a limit of 10 registered types; if this isn't enough, it could be increased.
//
// The methods described in QObjectHasInit and QObjectHasStatus are particularly useful
// for instantiated types to handle object creation and destruction.
//
// Instantiated objects are normal objects in every way, including for garbage collection.
func (c *Connection) RegisterTypeFactory(name string, t AnyQObject, factory func() AnyQObject) error {
	if c.started {
		return fmt.Errorf("Type '%s' must be registered before the connection starts", name)
	} else if len(c.instantiable) >= 10 {
		return fmt.Errorf("Type '%s' exceeds maximum of 10 instantiable types", name)
	} else if _, exists := c.instantiable[name]; exists {
		return fmt.Errorf("Type '%s' is already registered", name)
	}

	typeinfo, err := parseType(reflect.TypeOf(t))
	if err != nil {
		return err
	}
	typeinfo.Name = name

	c.instantiable[name] = instantiableType{
		Type:    typeinfo,
		Factory: factory,
	}
	return nil
}

// RegisterType registers a type to be creatable from QML. Instances of these types
// can be created, assigned properties, and used declaratively like any other QML type.
//
// New instances are copied from the template object, including its values. This means
// you can set fields for the instantiated type that are different from the zero value.
// This is equivalent to a Go value assignment; it does not perform a deep copy.
//
// RegisterType must be called before the connection starts (calling Process or Run).
// There is a limit of 10 registered types; if this isn't enough, it could be increased.
//
// The methods described in QObjectHasInit and QObjectHasStatus are particularly useful
// for instantiated types to handle object creation and destruction.
//
// Instantiated objects are normal objects in every way, including for garbage collection.
func (c *Connection) RegisterType(name string, template AnyQObject) error {
	t := reflect.Indirect(reflect.ValueOf(template))
	factory := func() AnyQObject {
		obj := reflect.New(t.Type())
		obj.Elem().Set(t)
		return obj.Interface().(AnyQObject)
	}
	return c.RegisterTypeFactory(name, template, factory)
}

func (c *Connection) typeIsAcknowledged(t *typeInfo) bool {
	_, exists := c.knownTypes[t.Name]
	return exists
}
