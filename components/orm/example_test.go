package orm_test

import (
	"fmt"
	orm "github.com/tiyee/holy/components/orm"
)

type exampleUser struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

func (*exampleUser) TableName() string { return "users" }
func (*exampleUser) Pk() string        { return "id" }

func ExampleORM_Fields() {
	users := orm.New(&exampleUser{})
	fmt.Println(users.TableName(), users.Pk())
	fmt.Println(users.Fields())
	// Output:
	// `users` `id`
	// `id`,`name`
}

func ExampleBinds() {
	user := &exampleUser{}
	binds := orm.Binds(user)
	*binds[1].(*string) = "alice"
	fmt.Println(user.Name)
	// Output: alice
}
