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
	"strings"

	uuid "github.com/satori/go.uuid"
)

type Connection struct {
	in           io.ReadCloser
	out          io.WriteCloser
	objects      map[string]*QObject
	instantiable map[string]instantiableType
	singletons   map[string]*QObject
	knownTypes   map[string]struct{}
	err          error

	started       bool
	processSignal chan struct{}
	queue         chan []byte

	syncSerial  int
	syncObjects int
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
		singletons:    make(map[string]*QObject),
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

// Number of object references after which a SYNC is sent to remove any
// unreferenced objects. 200 chosen at random.
const objectSyncThreshold = 200

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

	// REGISTER
	{
		types := make([]*typeInfo, 0, len(c.instantiable))
		for _, t := range c.instantiable {
			types = append(types, t.Type)
		}

		c.sendMessage(struct {
			messageBase
			Types      []*typeInfo         `json:"types"`
			Singletons map[string]*QObject `json:"singletons"`
		}{
			messageBase{"REGISTER"},
			types,
			c.singletons,
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

	for {
		var data []byte
		select {
		case data = <-c.queue:
		default:
			break
		}

		var msg map[string]interface{}
		if err := json.Unmarshal(data, &msg); err != nil {
			c.fatal("process invalid message: %s", err)
			// once queue is closed, the error from fatal will be returned
			continue
		}

		identifier := msg["identifier"].(string)
		impl, objExists := c.objects[identifier]

		switch msg["command"] {
		case "OBJECT_REF":
			if objExists {
				impl.clientRef = true
				// Record that the client has acknowledged an object of this type
				c.knownTypes[impl.typeInfo.Name] = struct{}{}
			} else {
				c.warn("ref of unknown object %s", identifier)
			}

		case "OBJECT_DEREF":
			if objExists {
				impl.clientRef = false
				if !impl.syncRef && !impl.syncPendingRef {
					c.removeObject(identifier, impl)
				}
			} else {
				c.warn("deref of unknown object %s", identifier)
			}

		case "SYNC_ACK":
			c.syncAck(msg["serial"].(int))

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
				impl = obj.qObject()
				impl.id = identifier
				impl.clientRef = true
				c.activateObject(obj)
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
						impl.id,
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
	}

	if c.err != nil {
		return c.err
	}

	if c.syncObjects > objectSyncThreshold {
		c.syncClient()
	}
	return nil
}

func (c *Connection) ProcessSignal() <-chan struct{} {
	c.ensureHandler()
	return c.processSignal
}

func (c *Connection) activateObject(obj AnyQObject) error {
	if err := Initialize(obj); err != nil {
		return err
	}

	q := obj.qObject()
	if q.c != nil {
		if q.c != c {
			// This situation is really not supported at all.
			return errors.New("object is already claimed by a different connection")
		}
	} else {
		q.c = c
		if q.id == "" {
			u, _ := uuid.NewV4()
			q.id = u.String()
		}
	}

	c.objects[q.id] = q
	if !q.syncRef {
		// Even if there is a clientRef, syncRef is needed to avoid a race when there
		// is a deref in flight.
		q.syncRef = true
		c.syncObjects++
	}

	if o, ok := obj.(QObjectHasActivation); ok {
		o.ObjectActivated()
	}
	return nil
}

func (c *Connection) syncClient() {
	if c.syncSerial != 0 || c.syncObjects == 0 {
		return
	}

	for _, q := range c.objects {
		if q.syncRef {
			q.syncRef = false
			q.syncPendingRef = true
		}
	}

	c.syncSerial++
	c.syncObjects = 0
	c.sendMessage(struct {
		messageBase
		Serial int `json:"serial"`
	}{
		messageBase{"SYNC"},
		c.syncSerial,
	})
}

func (c *Connection) syncAck(serial int) {
	if serial != c.syncSerial || c.syncSerial == 0 {
		c.fatal("Incorrect SYNC serial %d (expected %d)", serial, c.syncSerial)
		return
	}
	c.syncSerial = 0

	for id, q := range c.objects {
		if q.syncPendingRef {
			q.syncPendingRef = false
		}
		if !q.clientRef && !q.syncRef {
			c.removeObject(id, q)
		}
	}
}

func (c *Connection) removeObject(id string, q *QObject) {
	delete(c.objects, id)
	q.clientRef, q.syncRef, q.syncPendingRef = false, false, false
	q.c = nil
	if o, ok := q.object.(QObjectHasActivation); ok {
		o.ObjectDeactivated()
	}
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
		impl.id,
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
	}{messageBase{"EMIT"}, obj.id, method, data})
	return nil
}

// RegisterTypeFactory registers a type to be creatable from QML. Instances of these types
// can be created, assigned properties, and used declaratively like any other QML type.
//
// It is _not_ necessary to register QObject types that are simply given to QML. This
// registration allows new instances of a type to be created within QML in declarative
// syntax.
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
// It is _not_ necessary to register QObject types that are simply given to QML. This
// registration allows new instances of a type to be created within QML in declarative
// syntax.
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

// RegisterSingleton makes an object available globally in QML as a singleton. These
// objects always exist and can be used by their name anywhere within QML.
//
// Singletons must be registered before the connection is started, and the name must
// start with an uppercase letter, which is how they appear within QML, and must not
// conflict with any other type.
func (c *Connection) RegisterSingleton(name string, object AnyQObject) error {
	if c.started {
		return fmt.Errorf("Singleton '%s' must be registered before the connection starts", name)
	} else if len(name) < 1 || strings.ToUpper(string(name[0])) != string(name[0]) {
		return fmt.Errorf("Singleton name '%s' must start with an uppercase letter", name)
	} else if _, exists := c.singletons[name]; exists {
		return fmt.Errorf("Singleton '%s' is already registered", name)
	}

	q := object.qObject()
	q.id = name
	q.clientRef = true
	if err := c.activateObject(object); err != nil {
		return err
	}

	c.singletons[name] = q
	return nil
}

func (c *Connection) typeIsAcknowledged(t *typeInfo) bool {
	_, exists := c.knownTypes[t.Name]
	return exists
}
