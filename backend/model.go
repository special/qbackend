package qbackend

// Model is embedded in another type instead of QObject to create
// a data model, represented as a QAbstractItemModel to the client.
//
// To be a model, a type must embed Model and must implement the
// ModelDataSource interface. No other special initialization is
// necessary.
//
// When data changes, you must call Model's methods to notify the
// client of the change.
type Model struct {
	QObject
	// ModelAPI is an internal object for the model data API
	ModelAPI *modelAPI `json:"_qb_model"`
}

// XXX would like a struct based API here (primarily); maybe still an
// interface (serialize?) below that if desired
//
// or maybe that mostly gets left to SliceModel?

// Types embedding Model must implement ModelDataSource to provide data
type ModelDataSource interface {
	Row(row int) interface{}
	RowCount() int
	RoleNames() []string
}

// Types embedding Model _may_ implement ModelDataSourceRows to provide
// a list of all rows more efficiently.
//
// If the implementation of Rows() requires copying to a new slice, it may
// be more efficient to not implement this function.
type ModelDataSourceRows interface {
	ModelDataSource
	Rows() []interface{}
}

// modelAPI implements the internal qbackend API for model data; see QBackendModel from the plugin
type modelAPI struct {
	QObject
	Model     *Model `json:"-"`
	RoleNames []string
	BatchSize int

	// Signals
	ModelReset   func([]interface{}, int)      `qbackend:"rowData,moreRows"`
	ModelInsert  func(int, []interface{}, int) `qbackend:"start,rowData,moreRows"`
	ModelRemove  func(int, int)                `qbackend:"start,end"`
	ModelMove    func(int, int, int)           `qbackend:"start,end,destination"`
	ModelUpdate  func(int, interface{})        `qbackend:"row,data"`
	ModelRowData func(int, []interface{})      `qbackend:"start,rowData"`
}

func (m *modelAPI) Reset() {
	m.Model.Reset()
}

func (m *modelAPI) RequestRows(start, count int) {
	// BatchSize does not apply to RequestRows; the client asked for it
	rows, _ := m.getRows(start, count, 0)
	m.Emit("modelRowData", start, rows)
}

func (m *modelAPI) SetBatchSize(size int) {
	if size < 0 {
		size = 0
	}
	m.BatchSize = size
	m.Changed("BatchSize")
}

func (m *Model) dataSource() ModelDataSource {
	// The QObject interface is embedded in Model, so it can be accessed from here,
	// but Model is embedded in the app's model type as well, and that is the type
	// that is initialized for the QObject.
	//
	// This enables a neat trick: we can access the QObject here, and its Object
	// field will point back to the app's type, which is usually not available
	// from embedded types.
	impl, _ := asQObject(m)
	if impl == nil {
		return nil
	}

	if ds, ok := impl.object.(ModelDataSource); ok {
		return ds
	} else {
		// XXX Object must implement ModelRowSource; warn/error/panic/something
		return nil
	}
}

func (m *Model) InitObject() {
	data := m.dataSource()

	m.ModelAPI = &modelAPI{
		Model:     m,
		RoleNames: data.RoleNames(),
	}
	Initialize(m.ModelAPI)
}

func (m *modelAPI) getRows(start, count, batchSize int) ([]interface{}, int) {
	data := m.Model.dataSource()
	if data == nil {
		return []interface{}{}, 0
	}

	rowCount, moreRows := data.RowCount(), 0
	if start < 0 {
		start = 0
	} else if count < 0 {
		// negative count is for all (remaining) rows
		count = rowCount - start
	}
	if start+count > rowCount {
		if start >= rowCount {
			start = rowCount
		}
		count = rowCount - start
		if count < 0 {
			count = 0
		}
	}

	if batchSize > 0 && count > batchSize {
		moreRows = count - batchSize
		count = batchSize
	}

	if s, ok := data.(ModelDataSourceRows); ok {
		return s.Rows()[start:count], moreRows
	} else {
		rows := make([]interface{}, count)
		for i := 0; i < len(rows); i++ {
			rows[i] = data.Row(start + i)
		}
		return rows, moreRows
	}
}

func (m *Model) Reset() {
	rows, moreRows := m.ModelAPI.getRows(0, -1, m.ModelAPI.BatchSize)
	m.ModelAPI.Emit("modelReset", rows, moreRows)
}

func (m *Model) Inserted(start, count int) {
	rows, moreRows := m.ModelAPI.getRows(start, count, m.ModelAPI.BatchSize)
	m.ModelAPI.Emit("modelInsert", start, rows, moreRows)
}

func (m *Model) Removed(start, count int) {
	m.ModelAPI.Emit("modelRemove", start, start+count-1)
}

func (m *Model) Moved(start, count, destination int) {
	m.ModelAPI.Emit("modelMove", start, start+count-1, destination)
}

func (m *Model) Updated(row int) {
	data := m.dataSource()
	if data == nil {
		// No-op for uninitialized objects
		return
	}

	m.ModelAPI.Emit("modelUpdate", row, data.Row(row))
}
