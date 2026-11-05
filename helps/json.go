package helps

import (
	"encoding/json"
	"errors"
	"io"
)

func JSONArgs(r io.Reader, v interface{ Valid() error }) error {
	decoder := json.NewDecoder(r)
	if err := decoder.Decode(v); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err != nil {
			return err
		}
		return errors.New("json: expected one value")
	}
	return v.Valid()

}
