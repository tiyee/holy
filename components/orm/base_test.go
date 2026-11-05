package orm

import (
	"errors"
	"sync"
	"testing"
)

type cachedModel struct {
	ID   int64  `json:"id" db:"other_id,auto"`
	Name string `json:"name" db:"other_name"`
}

func (*cachedModel) TableName() string { return "cached_records" }
func (*cachedModel) Pk() string        { return "id" }

// Different structs may map different columns from the same table.
type cachedProjection struct {
	ID    int64 `json:"id"`
	Score int64 `json:"score"`
}

func (*cachedProjection) TableName() string { return "cached_records" }
func (*cachedProjection) Pk() string        { return "id" }

func TestLazyInstanceMetadata(t *testing.T) {
	var first *OrmV2[*cachedModel] = New(&cachedModel{})
	second := New(&cachedModel{})
	if first.meta != nil || second.meta != nil {
		t.Fatal("New eagerly loaded metadata")
	}
	if first.Fields() != "`id`,`name`" || second.Fields() != "`id`,`name`" {
		t.Fatal("legacy mapping changed")
	}
	meta := first.meta
	if meta == nil || second.meta != meta {
		t.Fatal("instances did not reuse the cached mapping")
	}
	first.Record = &cachedModel{Name: "replacement"}
	first.Fields()
	if first.meta != meta {
		t.Fatal("replacing a record of the same type discarded its mapping")
	}
	binds := scanBinds(first.Record, meta.fields)
	if binds[1] != &first.Record.Name || binds[1] == &second.Record.Name {
		t.Fatal("mapping cached record addresses")
	}
}

func TestMetadataAfterRecordTypeChange(t *testing.T) {
	m := New[ITable](&cachedModel{})
	original, err := m.findMeta()
	if err != nil {
		t.Fatal(err)
	}
	m.Record = &cachedProjection{}
	if m.Fields() != "`id`,`score`" || m.meta == original {
		t.Fatal("same table reused a different struct's mapping")
	}
	m.Record = valueModel{}
	if _, err := m.findMeta(); !errors.Is(err, ErrInvalidModel) {
		t.Fatalf("invalid replacement = %v", err)
	}
	m.Record = (*cachedModel)(nil)
	if _, err := m.findMeta(); !errors.Is(err, ErrInvalidModel) {
		t.Fatalf("nil replacement = %v", err)
	}
	m.Record = &cachedModel{}
	meta, err := m.findMeta()
	if err != nil || meta != original {
		t.Fatal("valid replacement failed to recover its mapping")
	}
}

type lazyConcurrentModel struct {
	ID int64 `json:"id"`
}

func (*lazyConcurrentModel) TableName() string { return "lazy_concurrent" }
func (*lazyConcurrentModel) Pk() string        { return "id" }

func TestConcurrentLazyMetadata(t *testing.T) {
	start := make(chan struct{})
	results := make(chan *metadata, 32)
	var wg sync.WaitGroup
	for range 32 {
		wg.Go(func() {
			m := New(&lazyConcurrentModel{})
			<-start
			meta, err := m.findMeta()
			if err != nil {
				t.Error(err)
				return
			}
			results <- meta
		})
	}
	close(start)
	wg.Wait()
	close(results)
	var first *metadata
	for meta := range results {
		if first == nil {
			first = meta
		}
		if meta != first || meta.columnSQL != "`id`" || len(meta.fields) != 1 {
			t.Fatal("concurrent lookup returned inconsistent metadata")
		}
	}
}

type valueModel struct {
	ID int64 `json:"id"`
}

func (valueModel) TableName() string { return "values" }
func (valueModel) Pk() string        { return "id" }
