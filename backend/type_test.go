package qbackend

import (
	"reflect"
	"testing"
)

type Simple struct {
	Simple string
}

type Fields struct {
	String    string
	Bytes     []byte
	Strings   []string
	Map       map[string]string
	Struct    Simple
	Ptr       *Simple
	Object    *TestStruct
	Interface interface{}
}

type TestStruct struct {
	QObject
	Fields

	unexported  bool
	Ignored     bool `qbackend:"-"`
	IgnoredJSON bool `json:"-"`

	Signal       func()
	SignalParams func(a, b int) `qbackend:"a,b"`
}

func (t *TestStruct) RealMethod(arg1 int, arg2 []string) (*TestStruct, error) {
	return t, nil
}

func TestParseTypes(t *testing.T) {
	obj := &TestStruct{}
	objType := reflect.TypeOf(*obj)
	info, err := parseType(objType)
	if err != nil {
		t.Errorf("parseType failed: %v", err)
	}

	t.Logf("parsed type: %s", info)

	expectProp := []string{"string", "bytes", "strings", "map", "struct", "ptr", "object", "interface"}
	expectMethod := []string{"realMethod"}
	expectSignal := []string{"signal", "signalParams"}

	for _, p := range expectProp {
		if _, exists := info.Properties[p]; !exists {
			t.Errorf("Expected property '%s' to exist", p)
		}

		expectSignal = append(expectSignal, typeFieldChangedName(p))
	}
	if len(expectProp) != len(info.Properties) {
		t.Errorf("Expected %d properties but type info has %d", len(expectProp), len(info.Properties))
	}

	for _, p := range expectMethod {
		if _, exists := info.Methods[p]; !exists {
			t.Errorf("Expected method '%s' to exist", p)
		}
	}
	if len(expectMethod) != len(info.Methods) {
		t.Errorf("Expected %d methods but type info has %d", len(expectMethod), len(info.Methods))
	}
	if m, ok := info.Methods["realMethod"]; ok {
		if len(m.Args) != 2 {
			t.Errorf("Method expected %d args but has %d: %v", 2, len(m.Args), m.Args)
		}
		if len(m.Return) != 1 {
			t.Errorf("Method expected %d return values but has %d: %v", 1, len(m.Return), m.Return)
		}
	} else {
		t.Errorf("Missing method 'realMethod'")
	}

	for _, p := range expectSignal {
		if _, exists := info.Signals[p]; !exists {
			t.Errorf("Expected signal '%s' to exist", p)
		}
	}
	if len(expectSignal) != len(info.Signals) {
		t.Errorf("Expected %d signals but type info has %d", len(expectSignal), len(info.Signals))
	}
}
