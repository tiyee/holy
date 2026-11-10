package orm

import (
	"errors"
	"reflect"
	"testing"
)

func TestMetadataValidation(t *testing.T) {
	for _, typ := range []reflect.Type{nil, reflect.TypeFor[valueModel](), reflect.TypeFor[*int]()} {
		if _, err := loadMetadata(typ, JSONTags); !errors.Is(err, ErrInvalidModel) {
			t.Fatalf("metadata for %v = %v", typ, err)
		}
	}
}

type metadataModel struct {
	ID        int64  `json:"id,omitempty" db:"other_id,auto"`
	Name      string `json:"name" db:"other_name"`
	Ignored   string `json:"-"`
	Transient string
	NoName    string `json:",omitempty"`
	hidden    string
}

func (*metadataModel) TableName() string { return "metadata_records" }
func (*metadataModel) Pk() string        { return "id" }

func TestMetadataJSONMapping(t *testing.T) {
	meta, err := loadMetadata(reflect.TypeFor[*metadataModel](), JSONTags)
	if err != nil {
		t.Fatal(err)
	}
	if meta.columnSQL != "`id`,`name`" || meta.insertRow != "(?,?)" || len(meta.fields) != 2 || meta.fields[1].index != 1 {
		t.Fatalf("metadata = %+v", meta)
	}
	record := &metadataModel{ID: 7, Name: "first"}
	values := fieldValues(record, meta.fields)
	if !reflect.DeepEqual(values, []any{int64(7), "first"}) {
		t.Fatal(values)
	}
	if quoteIdentifier("odd`column") != "`odd``column`" {
		t.Fatal("identifier not escaped")
	}
	cached, err := loadMetadata(reflect.TypeFor[*metadataModel](), JSONTags)
	if err != nil || cached != meta {
		t.Fatal("metadata not reused")
	}
}

func TestDatabaseTagMappingIsOptIn(t *testing.T) {
	m := New(&metadataModel{})
	db := m.WithMapping(DatabaseTags)
	if m.meta == nil || db.meta == nil ||
		m.meta.columnSQL != "`id`,`name`" || db.meta.columnSQL != "`other_id`,`other_name`" {
		t.Fatal("mapping modes overlap")
	}
	if db.record != m.record {
		t.Fatal("mapping copy replaced the record")
	}
	if cached := New(&metadataModel{}).WithMapping(DatabaseTags); cached.meta != db.meta {
		t.Fatal("database metadata not cached")
	}
	if err := m.WithMapping(MappingMode(255)).validateModel(); !errors.Is(err, ErrInvalidMapping) {
		t.Fatal(err)
	}
}

type duplicateColumns struct {
	First  int `json:"first" db:"same"`
	Second int `json:"second" db:"same"`
}

func TestDuplicateColumnMappingRejected(t *testing.T) {
	if _, err := loadMetadata(reflect.TypeFor[*duplicateColumns](), DatabaseTags); !errors.Is(err, ErrInvalidMapping) {
		t.Fatal(err)
	}
}

type dbOnlyModel struct {
	ID        int64  `db:"record_id" json:"publicId"`
	Internal  string `db:"secret" json:"-"`
	Transient string `db:"-" json:"transient"`
	Fallback  string `json:"fallback"`
}

func (*dbOnlyModel) TableName() string { return "db_records" }
func (*dbOnlyModel) Pk() string        { return "record_id" }
func TestDatabaseMappingWritesUseDatabasePrimaryKey(t *testing.T) {
	state := &testDatabase{}
	db := openTestDB(t, state)
	registerTestDB(t, db, db)
	m := New(&dbOnlyModel{ID: 7, Internal: "secret"}).WithMapping(DatabaseTags)
	if _, err := m.Update(7); err != nil {
		t.Fatal(err)
	}
	checkSQLCall(t, state.calls[0], "update `db_records` set `secret`=?,`fallback`=? where `record_id`=?", "secret", "", int64(7))
}

type mixedCaseKeyModel struct {
	ID   int64  `json:"ID" db:"Id"`
	Name string `json:"Name"`
}

func (*mixedCaseKeyModel) TableName() string { return "records" }
func (*mixedCaseKeyModel) Pk() string        { return "id" }

func TestUpdateSkipsMixedCasePrimaryKey(t *testing.T) {
	for _, mode := range []MappingMode{JSONTags, DatabaseTags} {
		state := &testDatabase{}
		m := New(&mixedCaseKeyModel{ID: 42}).WithMapping(mode).SetWDb(openTestDB(t, state))
		if _, err := m.Update(1); err != nil {
			t.Fatal(err)
		}
		checkSQLCall(t, state.calls[0], "update `records` set `Name`=? where `id`=?", "", int64(1))
	}
}
