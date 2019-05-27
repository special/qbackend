package qbackend

import "reflect"

// -> id, title, obj roles
type MyRow struct {
	Id    int
	Title string
	Obj   *MyObject
}

// serialize function implemented directly? avoids the overhead of restructuring data a bunch...
// role names lowercased too; they're just like properties
// have to set type early to get roles

// Let Model implement most of the logic, but allow overriding things for customized models..
// Or offer convenience models; not sure
// Can't rename types, so instantiable has to embed, but that's fine. Others could just use a Model..
// or a derived type for convenience

type SliceModel struct {
	Model

	roles   []string
	rows    []interface{}
	rowType reflect.Type
}

func NewSliceModel(rowType interface{}) *SliceModel {
	// rowType can be struct, map[string]value, or just a value ("modelData")
	// if rowType is a QObject, ... that maybe isn't unreasonable? but will require some weird special cases if so
	// if rowType is an array or slice, use its value type and use that data as rows
	if rowType == nil {
		panic("NewSliceModel with nil type")
	}

	// Unwrap types that contain values, in a specific order and non-recursively
	v := reflect.ValueOf(rowType)
	if v.Kind() == reflect.Interface {
		v = v.Elem()
	}
	if v.Kind() == reflect.Ptr {
		v = v.Elem()
	}
	if v.Kind() == reflect.Nil {
		panic("NewSliceModel with nil type")
	}

	// Unwrap to the value type for slices
	t := v.Type()
	hasData := false
	if v.Kind() == reflect.Slice || v.Kind() == reflect.Array {
		hasData = true
		t := t.Elem()
	}

	if t.Kind() == reflect.Map && t.Key().Kind() == reflect.String {
		panic("NewSliceModel is ambiguous and cannot determine roles from map[string]value. Use NewSliceModelRoles.")
	}

	var s *SliceModel
	switch t.Kind() {
	case reflect.Interface:
		fallthrough
	case reflect.Ptr:
		fallthrough
	case reflect.Chan:
		fallthrough
	case reflect.Func:
		fallthrough
	case reflect.UnsafePointer:
		panic("NewSliceModel given an invalid value type")

	case reflect.Struct:
		// XXX fields
		panic("NewSliceModel from struct not implemented")

	default:
		// All other types have a single 'modelData' role with one value per row.
		// There's no reason to actually care what the type is.
		s = NewSliceModelRoles([]string{"modelData"})
	}

	if hasData {
		s.Reset(v.Interface())
	}

	// ok cases:
	// - struct: use fields
	// - array or slice: unwrap and use element type; reset with data afterwards
	// - simple values: single modelData role
	// - maps with keys other than string: single modelData role
	// - multidimensional arrays/slices: single modelData role
	// - QObject: just treat as struct for now..
	//
	// fail cases:
	// - all map[string]value, regardless of slice: ambiguous, cannot determine roles; error
	// - unencodeable
	// - nil

	// XXX NewSliceModelRoles
}

func NewSliceModelRoles(roles []string) *SliceModel {
	s := &SliceModel{
		roles: roles,
	}
	Initialize(s)
	return s
}

func (s *SliceModel) RoleNames() []string {
	return s.roles
}

// XXX It really is kind of a problem that these can't be hidden from QML.. has to be a static setting somehow..

func (s *SliceModel) Reset(value interface{}) {
	if rows, ok := value.([]interface{}); ok {
		s.rows = rows
	} else {
		v := reflect.ValueOf(value)
		if v.Kind() != reflect.Slice && v.Kind() != reflect.Array {
			panic("SliceModel.Reset called with an invalid non-slice value")
		}
		// XXX consider type checking some? all? situations as in checking that v is
		// a []value, where value is what it was initialized as in NewSliceModel (if anything)
		// There's not a technical reason, really, but for sanity..
		s.rows = make([]interface{}, v.Len())
		for i := 0; i < len(s.rows); i++ {
			s.rows[i] = v.Index(i).Interface()
		}
	}

	// XXX signals and whatnot
}

func (s *SliceModel) Insert(pos int, rows interface{}) {
}

func (s *SliceModel) Remove(pos, count int) {
}

func (s *SliceModel) Update(row int, data interface{}) {
}

func (s *SliceModel) Move(pos, count, newPos int) {
}
