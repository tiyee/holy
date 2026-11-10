package orm

import (
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

func TestNewSharesMetadataAcrossRecords(t *testing.T) {
	firstRecord, secondRecord := &cachedModel{}, &cachedModel{}
	first, second := New(firstRecord), New(secondRecord)
	meta := first.meta
	if meta == nil || second.meta != meta {
		t.Fatal("instances did not reuse the cached mapping")
	}
	binds := make([]any, len(meta.fields))
	bindQueryInto(first.record, meta, binds)
	if binds[1] != &firstRecord.Name || binds[1] == &secondRecord.Name {
		t.Fatal("mapping cached record addresses")
	}
}

func TestNewUsesConcreteModelType(t *testing.T) {
	model := New[ITable](&cachedModel{})
	projection := New[ITable](&cachedProjection{})
	if model.meta == nil || projection.meta == nil ||
		model.meta.columnSQL != "`id`,`name`" || projection.meta.columnSQL != "`id`,`score`" {
		t.Fatal("same table reused a different struct's mapping")
	}
}

type concurrentModel struct {
	ID int64 `json:"id"`
}

func (*concurrentModel) TableName() string { return "concurrent_records" }
func (*concurrentModel) Pk() string        { return "id" }

func TestConcurrentMetadataInitialization(t *testing.T) {
	start := make(chan struct{})
	results := make(chan *metadata, 32)
	var wg sync.WaitGroup
	for range 32 {
		wg.Go(func() {
			<-start
			m := New(&concurrentModel{})
			if err := m.validateModel(); err != nil {
				t.Error(err)
				return
			}
			results <- m.meta
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
