package orm

import (
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"testing"
)

type rawBytesModel struct {
	Value    sql.RawBytes           `json:"value"`
	Optional *sql.RawBytes          `json:"optional"`
	Nullable sql.Null[sql.RawBytes] `json:"nullable"`
}

func (*rawBytesModel) TableName() string { return "records" }
func (*rawBytesModel) Pk() string        { return "id" }

func TestQueryOwnsRawBytes(t *testing.T) {
	for _, method := range []string{"Row", "Rows", "Limit"} {
		t.Run(method, func(t *testing.T) {
			source := []byte("first")
			state := &testDatabase{
				columns: []string{"value", "optional", "nullable"},
				data: [][]driver.Value{
					{"first", source, source}, {"later", []byte{}, []byte{}}, {nil, nil, nil},
				},
			}
			m := New(&rawBytesModel{}).SetRDb(openTestDB(t, state))
			if binds := Binds(m.Record); binds[0] != &m.Record.Value || binds[1] != &m.Record.Optional || binds[2] != &m.Record.Nullable {
				t.Fatal("Binds 应返回原字段地址")
			}
			var rows []*rawBytesModel
			var err error
			switch method {
			case "Row":
				var row *rawBytesModel
				row, err = m.Row("", nil)
				rows = []*rawBytesModel{row}
			case "Rows":
				rows, err = m.Rows("", nil)
			case "Limit":
				rows, err = m.Limit("", nil, 0, 3, "")
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(rows) == 0 || rows[0] == nil {
				t.Fatal("缺少首行")
			}
			// 模拟查询结束后驱动复用刚才返回的字节。
			copy(source, "reuse")
			first := rows[0]
			if string(first.Value) != "first" || first.Optional == nil || string(*first.Optional) != "first" ||
				!first.Nullable.Valid || string(first.Nullable.V) != "first" {
				t.Fatalf("首行数据被覆盖: %+v", first)
			}
			if method != "Row" {
				if len(rows) != 3 {
					t.Fatalf("结果行数 = %d", len(rows))
				}
				empty, null := rows[1], rows[2]
				if string(empty.Value) != "later" || empty.Optional == nil || *empty.Optional == nil || !empty.Nullable.Valid || empty.Nullable.V == nil {
					t.Fatalf("空字节值: %+v", empty)
				}
				if null.Value != nil || null.Optional != nil || null.Nullable.Valid {
					t.Fatalf("SQL NULL: %+v", null)
				}
			}
		})
	}
}

type scannerMap map[string]string

func (m *scannerMap) Scan(src any) error { return json.Unmarshal(src.([]byte), m) }

type scannerSlice []string

func (s *scannerSlice) Scan(src any) error { return json.Unmarshal(src.([]byte), s) }

type scannerDocument struct {
	Values map[string]string
}

func (d *scannerDocument) Scan(src any) error { return json.Unmarshal(src.([]byte), &d.Values) }

type scannerModel struct {
	Attrs    scannerMap      `json:"attrs"`
	Tags     scannerSlice    `json:"tags"`
	Document scannerDocument `json:"document"`
	ID       int64           `json:"id"`
	Template map[string]string
}

func (*scannerModel) TableName() string { return "scanner_records" }
func (*scannerModel) Pk() string        { return "id" }

func scannerData(name string, id driver.Value) []driver.Value {
	return []driver.Value{
		[]byte(`{"name":"` + name + `"}`),
		[]byte(`["` + name + `"]`),
		[]byte(`{"name":"` + name + `"}`),
		id,
	}
}

func TestScannerResultsAreIndependent(t *testing.T) {
	for _, method := range []string{"Rows", "Limit"} {
		for _, fail := range []bool{false, true} {
			t.Run(method+map[bool]string{false: "/success", true: "/scan failure"}[fail], func(t *testing.T) {
				state := &testDatabase{
					columns: []string{"attrs", "tags", "document", "id"},
					data:    [][]driver.Value{scannerData("template", int64(0))},
				}
				m := New(&scannerModel{Template: map[string]string{"keep": "value"}}).SetRDb(openTestDB(t, state))
				if _, err := m.Row("", nil); err != nil {
					t.Fatal(err)
				}
				state.data = [][]driver.Value{scannerData("first", int64(1)), scannerData("second", int64(2))}
				if fail {
					state.data[1][3] = "bad integer"
				}
				var rows []*scannerModel
				var err error
				if method == "Rows" {
					rows, err = m.Rows("", nil)
				} else {
					rows, err = m.Limit("", nil, 0, 2, "id")
				}
				if fail {
					if err == nil || rows != nil {
						t.Fatalf("failed scan = %+v, %v", rows, err)
					}
				} else {
					if err != nil || len(rows) != 2 {
						t.Fatalf("rows = %+v, %v", rows, err)
					}
					for i, name := range []string{"first", "second"} {
						if rows[i].Attrs["name"] != name || rows[i].Tags[0] != name || rows[i].Document.Values["name"] != name {
							t.Fatalf("row %d = %+v", i, rows[i])
						}
					}
					rows[0].Attrs["name"] = "changed"
					rows[0].Tags[0] = "changed"
					rows[0].Document.Values["name"] = "changed"
					if rows[1].Attrs["name"] != "second" || rows[1].Tags[0] != "second" || rows[1].Document.Values["name"] != "second" {
						t.Fatal("modifying the first row changed the second")
					}
					rows[0].Template["keep"] = "shared"
					if m.Record.Template["keep"] != "shared" || rows[1].Template["keep"] != "shared" {
						t.Fatal("unmapped template fields lost shallow-copy behavior")
					}
				}
				if m.Record.Attrs["name"] != "template" || m.Record.Tags[0] != "template" || m.Record.Document.Values["name"] != "template" {
					t.Fatalf("query mutated the template: %+v", m.Record)
				}
			})
		}
	}
}
