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
		"Saves": func(m *ORM[T]) error { _, err := m.Saves([]T{m.record}); return err },
		"SavesInBatches": func(m *ORM[T]) error {
			_, err := m.SavesInBatches([]T{m.record}, 1)
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
		{"typed nil", (*templateRecord)(nil)},
		{"struct value", valueModel{}},
	} {
		for name, operation := range databaseOperations[ITable]() {
			t.Run(model.name+"/"+name, func(t *testing.T) {
				state := &testDatabase{}
				db := openTestDB(t, state)
				m := New(model.record).SetDB(db)
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
