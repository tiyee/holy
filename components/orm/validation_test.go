package orm

import (
	"errors"
	"testing"
)

func databaseOperations[T ITable]() map[string]func(*ORM[T]) error {
	return map[string]func(*ORM[T]) error{
		"Row":   func(m *ORM[T]) error { _, err := m.Row("", nil); return err },
		"Rows":  func(m *ORM[T]) error { _, err := m.Rows("", nil); return err },
		"Limit": func(m *ORM[T]) error { _, err := m.Limit("", nil, 0, 1, ""); return err },
		"Count": func(m *ORM[T]) error { _, err := m.Count("", nil); return err },
		"Sum":   func(m *ORM[T]) error { _, err := m.Sum("score", "", nil); return err },
		"Save":  func(m *ORM[T]) error { _, err := m.Save(); return err },
		"Saves": func(m *ORM[T]) error { _, err := m.Saves([]T{m.Record}); return err },
		"SavesInBatches": func(m *ORM[T]) error {
			_, err := m.SavesInBatches([]T{m.Record}, 1)
			return err
		},
		"Update": func(m *ORM[T]) error { _, err := m.Update(1); return err },
		"UpdateByPk": func(m *ORM[T]) error {
			_, err := m.UpdateByPk(map[string]any{"name": "updated"}, 1)
			return err
		},
		"Delete": func(m *ORM[T]) error { _, err := m.Delete(1); return err },
	}
}

func TestInvalidModelsReturnErrors(t *testing.T) {
	for _, model := range []struct {
		name   string
		record ITable
	}{
		{"nil interface", nil},
		{"typed nil", (*compatibilityModel)(nil)},
		{"struct value", valueModel{}},
	} {
		for name, operation := range databaseOperations[ITable]() {
			t.Run(model.name+"/"+name, func(t *testing.T) {
				state := &testDatabase{}
				db := openTestDB(t, state)
				m := New[ITable](&compatibilityModel{table: "records"}).SetRDb(db).SetWDb(db)
				m.Fields() // Also exercise replacement after metadata has been cached.
				m.Record = model.record
				if err := operation(m); !errors.Is(err, ErrInvalidModel) {
					t.Fatalf("error = %v, want ErrInvalidModel", err)
				}
				if len(state.calls) != 0 {
					t.Fatal("invalid model reached the database")
				}
			})
		}
	}
}

func TestBindsRejectsNilRecord(t *testing.T) {
	defer func() {
		err, ok := recover().(error)
		if !ok || !errors.Is(err, ErrInvalidModel) {
			t.Fatalf("panic = %v, want ErrInvalidModel", err)
		}
	}()
	Binds((*legacyMethodModel)(nil))
}
