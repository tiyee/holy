package orm

import (
	"errors"
	"reflect"
)

// MySQL prepared statements encode the parameter count as an unsigned 16-bit integer.
const maxBatchParameters = 65535

func validateBatch[T ITable](rows []T, meta *metadata) error {
	for _, row := range rows {
		if isNil(row) || reflect.TypeOf(row) != meta.typ {
			return ErrInvalidModel
		}
	}
	return nil
}

// SavesInBatches 按 batchSize 和 MySQL 参数上限分批插入，首次写入前校验全部记录。
// 出错时返回已完成批次的 RowsAffected 累计值，不负责开启或结束事务。
// 跨批次原子性由 Repository 通过 AutoTransaction 编排，并将事务注入 ORM。
func (c *ORM[T]) SavesInBatches(rows []T, batchSize int) (int64, error) {
	if batchSize < 1 {
		return 0, ErrInvalidBatchSize
	}
	if len(rows) == 0 {
		return 0, nil
	}
	meta, err := c.findMeta()
	if err != nil {
		return 0, err
	}
	if _, err := c.insertQuery(meta); err != nil {
		return 0, err
	}
	if err := validateBatch(rows, meta); err != nil {
		return 0, err
	}
	maxRows := maxBatchParameters / len(meta.fields)
	if maxRows == 0 {
		return 0, errors.New("orm: mapped fields exceed MySQL parameter limit")
	}
	batchSize = min(batchSize, maxRows)
	var affected int64
	for start := 0; start < len(rows); {
		end := start + min(batchSize, len(rows)-start)
		n, err := c.Saves(rows[start:end])
		if err != nil {
			return affected, err
		}
		affected += n
		start = end
	}
	return affected, nil
}
