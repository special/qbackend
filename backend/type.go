package qbackend

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
)

// I cannot find any better way to filter the methods of the QObject interface
// from a type embedding that interface than this :/
var methodBlacklist []string = []string{
	"MarshalJSON",
	"Connection",
	"Referenced",
	"Emit",
	"ResetProperties",
	"Changed",
	"InitObject",
	"ObjectActivated",
	"ObjectDeactivated",
}

// typeInfo is the internal parsing and representation of a Go struct
// into a qbackend object type. It encodes into the typeinfo structure
// expected by the client as the value for an object type.
type typeInfo struct {
	Name       string                `json:"name"`
	Properties map[string]string     `json:"properties"`
	Methods    map[string]typeMethod `json:"methods"`
	Signals    map[string][]string   `json:"signals"`

	propertyFieldIndex map[string][]int
}

type typeMethod struct {
	// list of types for arguments
	Args []string `json:"args"`
	// type of return values
	Return []string `json:"return"`
}

var knownTypeInfo = make(map[reflect.Type]*typeInfo)
var qobjInterfaceType = reflect.TypeOf((*AnyQObject)(nil)).Elem()
var errorType = reflect.TypeOf((*error)(nil)).Elem()

func typeIsQObject(t reflect.Type) bool {
	return reflect.PtrTo(t).Implements(qobjInterfaceType)
}

func typeShouldIgnoreField(field reflect.StructField) bool {
	if field.PkgPath != "" || field.Tag.Get("qbackend") == "-" {
		// Unexported or ignored field
		return true
	} else if field.Type.Kind() != reflect.Func && field.Tag.Get("json") == "-" {
		// Non-signal property that isn't encoded by JSON
		return true
	} else if field.Name == "QObject" {
		return true
	} else {
		return false
	}
}

func typeShouldIgnoreMethod(method reflect.Method) bool {
	if method.PkgPath != "" {
		// Unexported
		return true
	}

	for _, badName := range methodBlacklist {
		if method.Name == badName {
			return true
		}
	}

	return false
}

func typeMethodName(method reflect.Method) string {
	name := method.Name
	if len(name) > 0 {
		name = strings.ToLower(string(name[0])) + name[1:]
	}
	return name
}

// Equivalent to Value.MethodByName, but handling typeMethodName rules
func typeMethodValueByName(v reflect.Value, name string) reflect.Value {
	t := v.Type()
	for i := 0; i < t.NumMethod(); i++ {
		method := t.Method(i)
		if method.Name == name || typeMethodName(method) == name {
			return v.Method(i)
		}
	}
	return reflect.ValueOf(nil)
}

func typeFieldName(field reflect.StructField) string {
	name := field.Name
	if len(name) > 0 {
		name = strings.ToLower(string(name[0])) + name[1:]
	}
	if field.Type.Kind() != reflect.Func {
		if tag := field.Tag.Get("json"); len(tag) > 0 {
			tags := strings.Split(tag, ",")
			if len(tags) > 0 && len(tags[0]) > 0 {
				name = tags[0]
			}
		}
	}
	return name
}

func typeFieldChangedName(fieldName string) string {
	return fieldName + "Changed"
}

func typeInfoTypeName(t reflect.Type) string {
	switch t.Kind() {
	case reflect.Ptr:
		return typeInfoTypeName(t.Elem())

	case reflect.Bool:
		return "bool"

	case reflect.Int:
		fallthrough
	case reflect.Int8:
		fallthrough
	case reflect.Int16:
		fallthrough
	case reflect.Int32:
		fallthrough
	case reflect.Int64:
		fallthrough
	case reflect.Uint:
		fallthrough
	case reflect.Uint8:
		fallthrough
	case reflect.Uint16:
		fallthrough
	case reflect.Uint32:
		fallthrough
	case reflect.Uint64:
		return "int"

	case reflect.Float32:
		fallthrough
	case reflect.Float64:
		return "double"

	case reflect.String:
		// TODO also []byte?
		return "string"

	case reflect.Array:
		fallthrough
	case reflect.Slice:
		return "array"

	case reflect.Map:
		return "map"

	case reflect.Struct:
		if typeIsQObject(t) {
			return "object"
		} else {
			return "map"
		}

	default:
		return "var"
	}
}

func parseType(t reflect.Type) (*typeInfo, error) {
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if typeInfo, exists := knownTypeInfo[t]; exists {
		return typeInfo, nil
	}

	if !typeIsQObject(t) {
		return nil, fmt.Errorf("Type '%s' is not a QObject; it must embed QObject or implement AnyQObject", t.Name())
	}

	typeInfo := &typeInfo{
		Properties:         make(map[string]string),
		Methods:            make(map[string]typeMethod),
		Signals:            make(map[string][]string),
		propertyFieldIndex: make(map[string][]int),
	}
	typeInfo.Name = t.Name()

	// Add properties and signals from fields, including those from anonymous
	// structs
	if err := typeFieldsToTypeInfo(typeInfo, t, []int{}); err != nil {
		return nil, err
	}

	// Create change signals for all properties, adopting explicit ones if they exist
	for name, _ := range typeInfo.Properties {
		signalName := typeFieldChangedName(name)
		if params, exists := typeInfo.Signals[signalName]; exists {
			if len(params) > 0 {
				return nil, fmt.Errorf("Signal '%s' is a property change signal, but has %d parameters. These signals should not have parameters.", signalName, len(params))
			}
		} else {
			typeInfo.Signals[signalName] = []string{}
		}
	}

	ptrType := reflect.PtrTo(t)
	for i := 0; i < ptrType.NumMethod(); i++ {
		method := ptrType.Method(i)
		methodType := method.Type
		if typeShouldIgnoreMethod(method) {
			continue
		}

		name := typeMethodName(method)
		var tm typeMethod

		for p := 1; p < methodType.NumIn(); p++ {
			inType := methodType.In(p)
			tm.Args = append(tm.Args, typeInfoTypeName(inType))
		}
		for p := 0; p < methodType.NumOut(); p++ {
			outType := methodType.Out(p)

			if p == methodType.NumOut()-1 && outType == errorType {
				// Skip the last output if it is an error; invoke will handle
				// these separately.
				continue
			}
			tm.Return = append(tm.Return, typeInfoTypeName(outType))
		}

		typeInfo.Methods[name] = tm
	}

	knownTypeInfo[t] = typeInfo
	return typeInfo, nil
}

func typeFieldsToTypeInfo(typeInfo *typeInfo, t reflect.Type, index []int) error {
	var anonStructs []reflect.StructField

	numFields := t.NumField()
	for i := 0; i < numFields; i++ {
		field := t.Field(i)
		if typeShouldIgnoreField(field) {
			continue
		} else if field.Anonymous {
			// Recurse into these at the end for breadth-first
			anonStructs = append(anonStructs, field)
			continue
		}
		name := typeFieldName(field)

		// Signals are represented by func properties, with a qbackend tag
		// giving a name for each parameter, which is required for QML.
		if field.Type.Kind() == reflect.Func {
			paramNames := strings.Split(field.Tag.Get("qbackend"), ",")
			if field.Type.NumIn() > 0 && len(paramNames) != field.Type.NumIn() {
				return fmt.Errorf("Signal '%s' has %d parameters, but names %d. All parameters must be named in the `qbackend:` tag.", name, field.Type.NumIn(), len(paramNames))
			}

			var params []string
			for p := 0; p < field.Type.NumIn(); p++ {
				inType := field.Type.In(p)
				params = append(params, typeInfoTypeName(inType)+" "+paramNames[p])
			}
			typeInfo.Signals[name] = params
		} else {
			typeInfo.Properties[name] = typeInfoTypeName(field.Type)
			typeInfo.propertyFieldIndex[name] = append(index, field.Index...)
		}
	}

	for _, ast := range anonStructs {
		at := ast.Type
		if at.Kind() == reflect.Ptr {
			at = at.Elem()
		}
		if err := typeFieldsToTypeInfo(typeInfo, at, append(index, ast.Index...)); err != nil {
			return err
		}
	}
	return nil
}

func (t *typeInfo) String() string {
	str, _ := json.MarshalIndent(t, "", "  ")
	return string(str)
}
