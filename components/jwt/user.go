package jwt

import (
	"encoding/json"
)

type User struct {
	Uid     int64  `json:"u"`
	Name    string `json:"n"`
	Expired int64  `json:"exp"`
}

func (u *User) Encode() []byte {
	if bs, err := json.Marshal(u); err == nil {
		return bs
	}
	return []byte{}

}
func (u *User) Decode(bs []byte) error {
	return json.Unmarshal(bs, u)
}
