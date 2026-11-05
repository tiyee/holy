package helps

import "testing"

type cloneFixture struct {
	ID      int64
	Name    string
	Values  []int
	Labels  map[string]string
	Pointer *int
	hidden  bool
}

func TestShallowClone(t *testing.T) {
	if got := ShallowClone[cloneFixture](nil); got != nil {
		t.Fatalf("nil clone = %+v", got)
	}
	number := 3
	source := &cloneFixture{ID: 1, Name: "source", Values: []int{2}, Labels: map[string]string{"key": "value"}, Pointer: &number, hidden: true}
	clone := ShallowClone(source)
	if clone == source || clone.ID != source.ID || clone.Name != source.Name || !clone.hidden {
		t.Fatalf("clone = %+v", clone)
	}
	clone.ID, clone.Name = 10, "clone"
	if source.ID != 1 || source.Name != "source" {
		t.Fatal("struct values were not copied independently")
	}
	clone.Values[0], clone.Labels["key"], *clone.Pointer = 4, "shared", 5
	if source.Values[0] != 4 || source.Labels["key"] != "shared" || number != 5 {
		t.Fatal("reference fields must retain shallow-copy semantics")
	}
	value := 7
	copy := ShallowClone(&value)
	*copy = 8
	if value != 7 {
		t.Fatal("scalar source was overwritten")
	}
}

var cloneBenchmarkSink *cloneFixture

func BenchmarkShallowClone(b *testing.B) {
	source := &cloneFixture{ID: 1, Name: "record"}
	b.Run("CopyPoint", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			cloneBenchmarkSink = CopyPoint(source)
		}
	})
	b.Run("ShallowClone", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			cloneBenchmarkSink = ShallowClone(source)
		}
	})
}
