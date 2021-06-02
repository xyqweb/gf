// Copyright GoFrame Author(https://goframe.org). All Rights Reserved.
//
// This Source Code Form is subject to the terms of the MIT License.
// If a copy of the MIT was not distributed with this file,
// You can obtain one at https://github.com/gogf/gf.

package gdb

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"

	"github.com/gogf/gf/container/gtype"
	"github.com/gogf/gf/os/gtime"
	"github.com/gogf/gf/util/gconv"
	"github.com/gogf/gf/util/guid"

	"github.com/gogf/gf/text/gregex"
)

// TX is the struct for transaction management.
type TX struct {
	db               DB              // db is the current gdb database manager.
	tx               *sql.Tx         // tx is the raw and underlying transaction manager.
	ctx              context.Context // ctx is the context for this transaction only.
	master           *sql.DB         // master is the raw and underlying database manager.
	transactionId    string          // transactionId is an unique id generated by this object for this transaction.
	transactionCount int             // transactionCount marks the times that Begins.
}

const (
	transactionPointerPrefix    = "transaction"
	contextTransactionKeyPrefix = "TransactionObjectForGroup_"
	transactionIdForLoggerCtx   = "TransactionId"
)

var (
	transactionIdGenerator = gtype.NewUint64()
)

// Begin starts and returns the transaction object.
// You should call Commit or Rollback functions of the transaction object
// if you no longer use the transaction. Commit or Rollback functions will also
// close the transaction automatically.
func (c *Core) Begin() (tx *TX, err error) {
	return c.doBeginCtx(c.GetCtx())
}

func (c *Core) doBeginCtx(ctx context.Context) (*TX, error) {
	if master, err := c.db.Master(); err != nil {
		return nil, err
	} else {
		var (
			tx         *TX
			sqlStr     = "BEGIN"
			mTime1     = gtime.TimestampMilli()
			rawTx, err = master.Begin()
			mTime2     = gtime.TimestampMilli()
			sqlObj     = &Sql{
				Sql:           sqlStr,
				Type:          "DB.Begin",
				Args:          nil,
				Format:        sqlStr,
				Error:         err,
				Start:         mTime1,
				End:           mTime2,
				Group:         c.db.GetGroup(),
				IsTransaction: true,
			}
		)
		if err == nil {
			tx = &TX{
				db:            c.db,
				tx:            rawTx,
				ctx:           context.WithValue(ctx, transactionIdForLoggerCtx, transactionIdGenerator.Add(1)),
				master:        master,
				transactionId: guid.S(),
			}
			ctx = tx.ctx
		}
		// Tracing and logging.
		c.addSqlToTracing(ctx, sqlObj)
		if c.db.GetDebug() {
			c.writeSqlToLogger(ctx, sqlObj)
		}
		return tx, err
	}
}

// Transaction wraps the transaction logic using function `f`.
// It rollbacks the transaction and returns the error from function `f` if
// it returns non-nil error. It commits the transaction and returns nil if
// function `f` returns nil.
//
// Note that, you should not Commit or Rollback the transaction in function `f`
// as it is automatically handled by this function.
func (c *Core) Transaction(ctx context.Context, f func(ctx context.Context, tx *TX) error) (err error) {
	var (
		tx *TX
	)
	if ctx == nil {
		ctx = c.GetCtx()
	}
	// Check transaction object from context.
	tx = TXFromCtx(ctx, c.db.GetGroup())
	if tx != nil {
		return tx.Transaction(ctx, f)
	}
	tx, err = c.doBeginCtx(ctx)
	if err != nil {
		return err
	}
	// Inject transaction object into context.
	tx.ctx = WithTX(tx.ctx, tx)
	defer func() {
		if err == nil {
			if e := recover(); e != nil {
				err = fmt.Errorf("%v", e)
			}
		}
		if err != nil {
			if e := tx.Rollback(); e != nil {
				err = e
			}
		} else {
			if e := tx.Commit(); e != nil {
				err = e
			}
		}
	}()
	err = f(tx.ctx, tx)
	return
}

// WithTX injects given transaction object into context and returns a new context.
func WithTX(ctx context.Context, tx *TX) context.Context {
	if tx == nil {
		return ctx
	}
	// Check repeat injection from given.
	group := tx.db.GetGroup()
	if tx := TXFromCtx(ctx, group); tx != nil && tx.db.GetGroup() == group {
		return ctx
	}
	dbCtx := tx.db.GetCtx()
	if tx := TXFromCtx(dbCtx, group); tx != nil && tx.db.GetGroup() == group {
		return dbCtx
	}
	// Inject transaction object and id into context.
	ctx = context.WithValue(ctx, transactionKeyForContext(group), tx)
	return ctx
}

// TXFromCtx retrieves and returns transaction object from context.
// It is usually used in nested transaction feature, and it returns nil if it is not set previously.
func TXFromCtx(ctx context.Context, group string) *TX {
	if ctx == nil {
		return nil
	}
	v := ctx.Value(transactionKeyForContext(group))
	if v != nil {
		tx := v.(*TX)
		tx.ctx = ctx
		return tx
	}
	return nil
}

// transactionKeyForContext forms and returns a string for storing transaction object of certain database group into context.
func transactionKeyForContext(group string) string {
	return contextTransactionKeyPrefix + group
}

// transactionKeyForNestedPoint forms and returns the transaction key at current save point.
func (tx *TX) transactionKeyForNestedPoint() string {
	return tx.db.GetCore().QuoteWord(transactionPointerPrefix + gconv.String(tx.transactionCount))
}

// Ctx sets the context for current transaction.
func (tx *TX) Ctx(ctx context.Context) *TX {
	tx.ctx = ctx
	return tx
}

// Commit commits current transaction.
// Note that it releases previous saved transaction point if it's in a nested transaction procedure,
// or else it commits the hole transaction.
func (tx *TX) Commit() error {
	if tx.transactionCount > 0 {
		tx.transactionCount--
		_, err := tx.Exec("RELEASE SAVEPOINT " + tx.transactionKeyForNestedPoint())
		return err
	}
	var (
		sqlStr = "COMMIT"
		mTime1 = gtime.TimestampMilli()
		err    = tx.tx.Commit()
		mTime2 = gtime.TimestampMilli()
		sqlObj = &Sql{
			Sql:           sqlStr,
			Type:          "TX.Commit",
			Args:          nil,
			Format:        sqlStr,
			Error:         err,
			Start:         mTime1,
			End:           mTime2,
			Group:         tx.db.GetGroup(),
			IsTransaction: true,
		}
	)
	tx.db.GetCore().addSqlToTracing(tx.ctx, sqlObj)
	if tx.db.GetDebug() {
		tx.db.GetCore().writeSqlToLogger(tx.ctx, sqlObj)
	}
	return err
}

// Rollback aborts current transaction.
// Note that it aborts current transaction if it's in a nested transaction procedure,
// or else it aborts the hole transaction.
func (tx *TX) Rollback() error {
	if tx.transactionCount > 0 {
		tx.transactionCount--
		_, err := tx.Exec("ROLLBACK TO SAVEPOINT " + tx.transactionKeyForNestedPoint())
		return err
	}
	var (
		sqlStr = "ROLLBACK"
		mTime1 = gtime.TimestampMilli()
		err    = tx.tx.Rollback()
		mTime2 = gtime.TimestampMilli()
		sqlObj = &Sql{
			Sql:           sqlStr,
			Type:          "TX.Rollback",
			Args:          nil,
			Format:        sqlStr,
			Error:         err,
			Start:         mTime1,
			End:           mTime2,
			Group:         tx.db.GetGroup(),
			IsTransaction: true,
		}
	)
	tx.db.GetCore().addSqlToTracing(tx.ctx, sqlObj)
	if tx.db.GetDebug() {
		tx.db.GetCore().writeSqlToLogger(tx.ctx, sqlObj)
	}
	return err
}

// Begin starts a nested transaction procedure.
func (tx *TX) Begin() error {
	_, err := tx.Exec("SAVEPOINT " + tx.transactionKeyForNestedPoint())
	if err != nil {
		return err
	}
	tx.transactionCount++
	return nil
}

// SavePoint performs `SAVEPOINT xxx` SQL statement that saves transaction at current point.
// The parameter `point` specifies the point name that will be saved to server.
func (tx *TX) SavePoint(point string) error {
	_, err := tx.Exec("SAVEPOINT " + tx.db.GetCore().QuoteWord(point))
	return err
}

// RollbackTo performs `ROLLBACK TO SAVEPOINT xxx` SQL statement that rollbacks to specified saved transaction.
// The parameter `point` specifies the point name that was saved previously.
func (tx *TX) RollbackTo(point string) error {
	_, err := tx.Exec("ROLLBACK TO SAVEPOINT " + tx.db.GetCore().QuoteWord(point))
	return err
}

// Transaction wraps the transaction logic using function `f`.
// It rollbacks the transaction and returns the error from function `f` if
// it returns non-nil error. It commits the transaction and returns nil if
// function `f` returns nil.
//
// Note that, you should not Commit or Rollback the transaction in function `f`
// as it is automatically handled by this function.
func (tx *TX) Transaction(ctx context.Context, f func(ctx context.Context, tx *TX) error) (err error) {
	if ctx != nil {
		tx.ctx = ctx
	}
	// Check transaction object from context.
	if TXFromCtx(tx.ctx, tx.db.GetGroup()) == nil {
		// Inject transaction object into context.
		tx.ctx = WithTX(tx.ctx, tx)
	}
	err = tx.Begin()
	if err != nil {
		return err
	}
	defer func() {
		if err == nil {
			if e := recover(); e != nil {
				err = fmt.Errorf("%v", e)
			}
		}
		if err != nil {
			if e := tx.Rollback(); e != nil {
				err = e
			}
		} else {
			if e := tx.Commit(); e != nil {
				err = e
			}
		}
	}()
	err = f(tx.ctx, tx)
	return
}

// Query does query operation on transaction.
// See Core.Query.
func (tx *TX) Query(sql string, args ...interface{}) (rows *sql.Rows, err error) {
	return tx.db.GetCore().DoQuery(tx.ctx, &txLink{tx.tx}, sql, args...)
}

// Exec does none query operation on transaction.
// See Core.Exec.
func (tx *TX) Exec(sql string, args ...interface{}) (sql.Result, error) {
	return tx.db.GetCore().DoExec(tx.ctx, &txLink{tx.tx}, sql, args...)
}

// Prepare creates a prepared statement for later queries or executions.
// Multiple queries or executions may be run concurrently from the
// returned statement.
// The caller must call the statement's Close method
// when the statement is no longer needed.
func (tx *TX) Prepare(sql string) (*Stmt, error) {
	return tx.db.GetCore().DoPrepare(tx.ctx, &txLink{tx.tx}, sql)
}

// GetAll queries and returns data records from database.
func (tx *TX) GetAll(sql string, args ...interface{}) (Result, error) {
	rows, err := tx.Query(sql, args...)
	if err != nil || rows == nil {
		return nil, err
	}
	defer rows.Close()
	return tx.db.GetCore().convertRowsToResult(rows)
}

// GetOne queries and returns one record from database.
func (tx *TX) GetOne(sql string, args ...interface{}) (Record, error) {
	list, err := tx.GetAll(sql, args...)
	if err != nil {
		return nil, err
	}
	if len(list) > 0 {
		return list[0], nil
	}
	return nil, nil
}

// GetStruct queries one record from database and converts it to given struct.
// The parameter `pointer` should be a pointer to struct.
func (tx *TX) GetStruct(obj interface{}, sql string, args ...interface{}) error {
	one, err := tx.GetOne(sql, args...)
	if err != nil {
		return err
	}
	return one.Struct(obj)
}

// GetStructs queries records from database and converts them to given struct.
// The parameter `pointer` should be type of struct slice: []struct/[]*struct.
func (tx *TX) GetStructs(objPointerSlice interface{}, sql string, args ...interface{}) error {
	all, err := tx.GetAll(sql, args...)
	if err != nil {
		return err
	}
	return all.Structs(objPointerSlice)
}

// GetScan queries one or more records from database and converts them to given struct or
// struct array.
//
// If parameter `pointer` is type of struct pointer, it calls GetStruct internally for
// the conversion. If parameter `pointer` is type of slice, it calls GetStructs internally
// for conversion.
func (tx *TX) GetScan(pointer interface{}, sql string, args ...interface{}) error {
	t := reflect.TypeOf(pointer)
	k := t.Kind()
	if k != reflect.Ptr {
		return fmt.Errorf("params should be type of pointer, but got: %v", k)
	}
	k = t.Elem().Kind()
	switch k {
	case reflect.Array, reflect.Slice:
		return tx.GetStructs(pointer, sql, args...)
	case reflect.Struct:
		return tx.GetStruct(pointer, sql, args...)
	default:
		return fmt.Errorf("element type should be type of struct/slice, unsupported: %v", k)
	}
}

// GetValue queries and returns the field value from database.
// The sql should queries only one field from database, or else it returns only one
// field of the result.
func (tx *TX) GetValue(sql string, args ...interface{}) (Value, error) {
	one, err := tx.GetOne(sql, args...)
	if err != nil {
		return nil, err
	}
	for _, v := range one {
		return v, nil
	}
	return nil, nil
}

// GetCount queries and returns the count from database.
func (tx *TX) GetCount(sql string, args ...interface{}) (int, error) {
	if !gregex.IsMatchString(`(?i)SELECT\s+COUNT\(.+\)\s+FROM`, sql) {
		sql, _ = gregex.ReplaceString(`(?i)(SELECT)\s+(.+)\s+(FROM)`, `$1 COUNT($2) $3`, sql)
	}
	value, err := tx.GetValue(sql, args...)
	if err != nil {
		return 0, err
	}
	return value.Int(), nil
}

// Insert does "INSERT INTO ..." statement for the table.
// If there's already one unique record of the data in the table, it returns error.
//
// The parameter `data` can be type of map/gmap/struct/*struct/[]map/[]struct, etc.
// Eg:
// Data(g.Map{"uid": 10000, "name":"john"})
// Data(g.Slice{g.Map{"uid": 10000, "name":"john"}, g.Map{"uid": 20000, "name":"smith"})
//
// The parameter `batch` specifies the batch operation count when given data is slice.
func (tx *TX) Insert(table string, data interface{}, batch ...int) (sql.Result, error) {
	if len(batch) > 0 {
		return tx.Model(table).Ctx(tx.ctx).Data(data).Batch(batch[0]).Insert()
	}
	return tx.Model(table).Ctx(tx.ctx).Data(data).Insert()
}

// InsertIgnore does "INSERT IGNORE INTO ..." statement for the table.
// If there's already one unique record of the data in the table, it ignores the inserting.
//
// The parameter `data` can be type of map/gmap/struct/*struct/[]map/[]struct, etc.
// Eg:
// Data(g.Map{"uid": 10000, "name":"john"})
// Data(g.Slice{g.Map{"uid": 10000, "name":"john"}, g.Map{"uid": 20000, "name":"smith"})
//
// The parameter `batch` specifies the batch operation count when given data is slice.
func (tx *TX) InsertIgnore(table string, data interface{}, batch ...int) (sql.Result, error) {
	if len(batch) > 0 {
		return tx.Model(table).Ctx(tx.ctx).Data(data).Batch(batch[0]).InsertIgnore()
	}
	return tx.Model(table).Ctx(tx.ctx).Data(data).InsertIgnore()
}

// InsertAndGetId performs action Insert and returns the last insert id that automatically generated.
func (tx *TX) InsertAndGetId(table string, data interface{}, batch ...int) (int64, error) {
	if len(batch) > 0 {
		return tx.Model(table).Ctx(tx.ctx).Data(data).Batch(batch[0]).InsertAndGetId()
	}
	return tx.Model(table).Ctx(tx.ctx).Data(data).InsertAndGetId()
}

// Replace does "REPLACE INTO ..." statement for the table.
// If there's already one unique record of the data in the table, it deletes the record
// and inserts a new one.
//
// The parameter `data` can be type of map/gmap/struct/*struct/[]map/[]struct, etc.
// Eg:
// Data(g.Map{"uid": 10000, "name":"john"})
// Data(g.Slice{g.Map{"uid": 10000, "name":"john"}, g.Map{"uid": 20000, "name":"smith"})
//
// The parameter `data` can be type of map/gmap/struct/*struct/[]map/[]struct, etc.
// If given data is type of slice, it then does batch replacing, and the optional parameter
// `batch` specifies the batch operation count.
func (tx *TX) Replace(table string, data interface{}, batch ...int) (sql.Result, error) {
	if len(batch) > 0 {
		return tx.Model(table).Ctx(tx.ctx).Data(data).Batch(batch[0]).Replace()
	}
	return tx.Model(table).Ctx(tx.ctx).Data(data).Replace()
}

// Save does "INSERT INTO ... ON DUPLICATE KEY UPDATE..." statement for the table.
// It updates the record if there's primary or unique index in the saving data,
// or else it inserts a new record into the table.
//
// The parameter `data` can be type of map/gmap/struct/*struct/[]map/[]struct, etc.
// Eg:
// Data(g.Map{"uid": 10000, "name":"john"})
// Data(g.Slice{g.Map{"uid": 10000, "name":"john"}, g.Map{"uid": 20000, "name":"smith"})
//
// If given data is type of slice, it then does batch saving, and the optional parameter
// `batch` specifies the batch operation count.
func (tx *TX) Save(table string, data interface{}, batch ...int) (sql.Result, error) {
	if len(batch) > 0 {
		return tx.Model(table).Ctx(tx.ctx).Data(data).Batch(batch[0]).Save()
	}
	return tx.Model(table).Ctx(tx.ctx).Data(data).Save()
}

// BatchInsert batch inserts data.
// The parameter `list` must be type of slice of map or struct.
func (tx *TX) BatchInsert(table string, list interface{}, batch ...int) (sql.Result, error) {
	if len(batch) > 0 {
		return tx.Model(table).Ctx(tx.ctx).Data(list).Batch(batch[0]).Insert()
	}
	return tx.Model(table).Ctx(tx.ctx).Data(list).Insert()
}

// BatchInsertIgnore batch inserts data with ignore option.
// The parameter `list` must be type of slice of map or struct.
func (tx *TX) BatchInsertIgnore(table string, list interface{}, batch ...int) (sql.Result, error) {
	if len(batch) > 0 {
		return tx.Model(table).Ctx(tx.ctx).Data(list).Batch(batch[0]).InsertIgnore()
	}
	return tx.Model(table).Ctx(tx.ctx).Data(list).InsertIgnore()
}

// BatchReplace batch replaces data.
// The parameter `list` must be type of slice of map or struct.
func (tx *TX) BatchReplace(table string, list interface{}, batch ...int) (sql.Result, error) {
	if len(batch) > 0 {
		return tx.Model(table).Ctx(tx.ctx).Data(list).Batch(batch[0]).Replace()
	}
	return tx.Model(table).Ctx(tx.ctx).Data(list).Replace()
}

// BatchSave batch replaces data.
// The parameter `list` must be type of slice of map or struct.
func (tx *TX) BatchSave(table string, list interface{}, batch ...int) (sql.Result, error) {
	if len(batch) > 0 {
		return tx.Model(table).Ctx(tx.ctx).Data(list).Batch(batch[0]).Save()
	}
	return tx.Model(table).Ctx(tx.ctx).Data(list).Save()
}

// Update does "UPDATE ... " statement for the table.
//
// The parameter `data` can be type of string/map/gmap/struct/*struct, etc.
// Eg: "uid=10000", "uid", 10000, g.Map{"uid": 10000, "name":"john"}
//
// The parameter `condition` can be type of string/map/gmap/slice/struct/*struct, etc.
// It is commonly used with parameter `args`.
// Eg:
// "uid=10000",
// "uid", 10000
// "money>? AND name like ?", 99999, "vip_%"
// "status IN (?)", g.Slice{1,2,3}
// "age IN(?,?)", 18, 50
// User{ Id : 1, UserName : "john"}
func (tx *TX) Update(table string, data interface{}, condition interface{}, args ...interface{}) (sql.Result, error) {
	return tx.Model(table).Ctx(tx.ctx).Data(data).Where(condition, args...).Update()
}

// Delete does "DELETE FROM ... " statement for the table.
//
// The parameter `condition` can be type of string/map/gmap/slice/struct/*struct, etc.
// It is commonly used with parameter `args`.
// Eg:
// "uid=10000",
// "uid", 10000
// "money>? AND name like ?", 99999, "vip_%"
// "status IN (?)", g.Slice{1,2,3}
// "age IN(?,?)", 18, 50
// User{ Id : 1, UserName : "john"}
func (tx *TX) Delete(table string, condition interface{}, args ...interface{}) (sql.Result, error) {
	return tx.Model(table).Ctx(tx.ctx).Where(condition, args...).Delete()
}
