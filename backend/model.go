package qbackend

// XXX Is there any reason for Model to _be_ the object, versus being a
// special thing placed into the object?
// XXX ^ trying this; we'll have "model is a field by the name Model"?
// kinda weird and magical..

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

	Data ModelData

	// ModelAPI is an internal object for the model data API
	ModelAPI *modelAPI `json:"_qb_model"`
}

// XXX eh, not sure what the point is for any of this

type ModelData interface {
	Row(row int) interface{}
	RowCount() int
	RoleNames() []string
}

type ModelDataRows interface {
	ModelData
	Rows(i, j int) []interface{}
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

func (m *Model) InitObject() {
	// XXX issues
	data := m.dataSource()

	m.ModelAPI = &modelAPI{
		Model:     m,
		RoleNames: data.RoleNames(),
	}

	// Initialize ModelAPI right away as well
	m.Connection().InitObject(m.ModelAPI)
}

func (m *modelAPI) getRows(start, count, batchSize int) ([]interface{}, int) {
	data := m.Model.Data
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

	if s, ok := data.(ModelDataRows); ok {
		return s.Rows(start, start+count), moreRows
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
	if m.Data == nil {
		// No-op for uninitialized objects
		return
	}

	m.ModelAPI.Emit("modelUpdate", row, m.Data.Row(row))
}
