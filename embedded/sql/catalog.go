/*
Copyright 2022 Codenotary Inc. All rights reserved.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

	http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package sql

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"strings"
	"time"

	"github.com/codenotary/immudb/embedded/store"
)

type Catalog struct {
	dbsByID   map[uint32]*Database
	dbsByName map[string]*Database
}

type Database struct {
	id           uint32
	catalog      *Catalog
	name         string
	tables       []*Table
	tablesByID   map[uint32]*Table
	tablesByName map[string]*Table
}

type Table struct {
	db              *Database
	id              uint32
	name            string
	cols            []*Column
	colsByID        map[uint32]*Column
	colsByName      map[string]*Column
	indexes         []*Index
	indexesByName   map[string]*Index
	indexesByColID  map[uint32][]*Index
	primaryIndex    *Index
	autoIncrementPK bool
	maxPK           int64
}

type Index struct {
	table    *Table
	id       uint32
	unique   bool
	cols     []*Column
	colsByID map[uint32]*Column
}

type Column struct {
	table         *Table
	id            uint32
	colName       string
	colType       SQLValueType
	maxLen        int
	autoIncrement bool
	notNull       bool
}

func newCatalog() *Catalog {
	return &Catalog{
		dbsByID:   map[uint32]*Database{},
		dbsByName: map[string]*Database{},
	}
}

func (c *Catalog) ExistDatabase(db string) bool {
	_, exists := c.dbsByName[db]
	return exists
}

func (c *Catalog) newDatabase(id uint32, name string) (*Database, error) {
	exists := c.ExistDatabase(name)
	if exists {
		return nil, ErrDatabaseAlreadyExists
	}

	db := &Database{
		id:           id,
		catalog:      c,
		name:         name,
		tablesByID:   map[uint32]*Table{},
		tablesByName: map[string]*Table{},
	}

	c.dbsByID[db.id] = db
	c.dbsByName[db.name] = db

	return db, nil
}

func (c *Catalog) Databases() []*Database {
	dbs := make([]*Database, len(c.dbsByID))

	i := 0
	for _, db := range c.dbsByID {
		dbs[i] = db
		i++
	}

	return dbs
}

func (c *Catalog) GetDatabaseByName(name string) (*Database, error) {
	db, exists := c.dbsByName[name]
	if !exists {
		return nil, ErrDatabaseDoesNotExist
	}
	return db, nil
}

func (c *Catalog) GetDatabaseByID(id uint32) (*Database, error) {
	db, exists := c.dbsByID[id]
	if !exists {
		return nil, ErrDatabaseDoesNotExist
	}
	return db, nil
}

func (db *Database) ID() uint32 {
	return db.id
}

func (db *Database) Name() string {
	return db.name
}

func (db *Database) ExistTable(table string) bool {
	_, exists := db.tablesByName[table]
	return exists
}

func (c *Catalog) GetTableByName(dbName, tableName string) (*Table, error) {
	db, err := c.GetDatabaseByName(dbName)
	if err != nil {
		return nil, err
	}
	return db.GetTableByName(tableName)
}

func (db *Database) GetTables() []*Table {
	return db.tables
}

func (db *Database) GetTableByName(name string) (*Table, error) {
	table, exists := db.tablesByName[name]
	if !exists {
		return nil, fmt.Errorf("%w (%s)", ErrTableDoesNotExist, name)
	}
	return table, nil
}

func (db *Database) GetTableByID(id uint32) (*Table, error) {
	table, exists := db.tablesByID[id]
	if !exists {
		return nil, ErrTableDoesNotExist
	}
	return table, nil
}

func (t *Table) ID() uint32 {
	return t.id
}

func (t *Table) Database() *Database {
	return t.db
}

func (t *Table) Cols() []*Column {
	return t.cols
}

func (t *Table) ColsByName() map[string]*Column {
	return t.colsByName
}

func (t *Table) Name() string {
	return t.name
}

func (t *Table) PrimaryIndex() *Index {
	return t.primaryIndex
}

func (t *Table) IsIndexed(colName string) (indexed bool, err error) {
	c, exists := t.colsByName[colName]
	if !exists {
		return false, fmt.Errorf("%w (%s)", ErrColumnDoesNotExist, colName)
	}

	_, ok := t.indexesByColID[c.id]

	return ok, nil
}

func (t *Table) IndexesByColID(colID uint32) []*Index {
	return t.indexesByColID[colID]
}

func (t *Table) GetColumnByName(name string) (*Column, error) {
	col, exists := t.colsByName[name]
	if !exists {
		return nil, fmt.Errorf("%w (%s)", ErrColumnDoesNotExist, name)
	}
	return col, nil
}

func (t *Table) GetColumnByID(id uint32) (*Column, error) {
	col, exists := t.colsByID[id]
	if !exists {
		return nil, ErrColumnDoesNotExist
	}
	return col, nil
}

func (i *Index) IsPrimary() bool {
	return i.id == PKIndexID
}

func (i *Index) IsUnique() bool {
	return i.unique
}

func (i *Index) Cols() []*Column {
	return i.cols
}

func (i *Index) IncludesCol(colID uint32) bool {
	_, ok := i.colsByID[colID]
	return ok
}

func (i *Index) sortableUsing(colID uint32, rangesByColID map[uint32]*typedValueRange) bool {
	// all columns before colID must be fixedValues otherwise the index can not be used
	for _, col := range i.cols {
		if col.id == colID {
			return true
		}

		colRange, ok := rangesByColID[col.id]
		if ok && colRange.unitary() {
			continue
		}

		return false
	}
	return false
}

func (i *Index) prefix() string {
	if i.IsPrimary() {
		return PIndexPrefix
	}

	if i.IsUnique() {
		return UIndexPrefix
	}

	return SIndexPrefix
}

func (i *Index) Name() string {
	return indexName(i.table.name, i.cols)
}

func indexName(tableName string, cols []*Column) string {
	var buf strings.Builder

	buf.WriteString(tableName)

	buf.WriteString("[")

	for c, col := range cols {
		buf.WriteString(col.colName)

		if c < len(cols)-1 {
			buf.WriteString(",")
		}
	}

	buf.WriteString("]")

	return buf.String()
}

func (db *Database) newTable(name string, colsSpec []*ColSpec) (table *Table, err error) {
	if len(name) == 0 || len(colsSpec) == 0 {
		return nil, ErrIllegalArguments
	}

	exists := db.ExistTable(name)
	if exists {
		return nil, fmt.Errorf("%w (%s)", ErrTableAlreadyExists, name)
	}

	id := len(db.tables) + 1

	table = &Table{
		id:             uint32(id),
		db:             db,
		name:           name,
		cols:           make([]*Column, len(colsSpec)),
		colsByID:       make(map[uint32]*Column),
		colsByName:     make(map[string]*Column),
		indexesByName:  make(map[string]*Index),
		indexesByColID: make(map[uint32][]*Index),
	}

	for i, cs := range colsSpec {
		_, colExists := table.colsByName[cs.colName]
		if colExists {
			return nil, ErrDuplicatedColumn
		}

		if cs.autoIncrement && cs.colType != IntegerType {
			return nil, ErrLimitedAutoIncrement
		}

		if !validMaxLenForType(cs.maxLen, cs.colType) {
			return nil, ErrLimitedMaxLen
		}

		id := len(table.colsByID) + 1

		col := &Column{
			id:            uint32(id),
			table:         table,
			colName:       cs.colName,
			colType:       cs.colType,
			maxLen:        cs.maxLen,
			autoIncrement: cs.autoIncrement,
			notNull:       cs.notNull,
		}

		table.cols[i] = col
		table.colsByID[col.id] = col
		table.colsByName[col.colName] = col
	}

	db.tables = append(db.tables, table)
	db.tablesByID[table.id] = table
	db.tablesByName[table.name] = table

	return table, nil
}

func (t *Table) newIndex(unique bool, colIDs []uint32) (index *Index, err error) {
	if len(colIDs) < 1 {
		return nil, ErrIllegalArguments
	}

	// validate column ids
	cols := make([]*Column, len(colIDs))
	colsByID := make(map[uint32]*Column, len(colIDs))

	for i, colID := range colIDs {
		col, err := t.GetColumnByID(colID)
		if err != nil {
			return nil, err
		}

		_, ok := colsByID[colID]
		if ok {
			return nil, ErrDuplicatedColumn
		}

		cols[i] = col
		colsByID[colID] = col
	}

	index = &Index{
		id:       uint32(len(t.indexes)),
		table:    t,
		unique:   unique,
		cols:     cols,
		colsByID: colsByID,
	}

	_, exists := t.indexesByName[index.Name()]
	if exists {
		return nil, ErrIndexAlreadyExists
	}

	t.indexes = append(t.indexes, index)
	t.indexesByName[index.Name()] = index

	// having a direct way to get the indexes by colID
	for _, col := range index.cols {
		t.indexesByColID[col.id] = append(t.indexesByColID[col.id], index)
	}

	if index.id == PKIndexID {
		t.primaryIndex = index
		t.autoIncrementPK = len(index.cols) == 1 && index.cols[0].autoIncrement
	}

	return index, nil
}

func (t *Table) newColumn(spec *ColSpec) (*Column, error) {
	if spec.autoIncrement {
		return nil, fmt.Errorf("%w (%s)", ErrLimitedAutoIncrement, spec.colName)
	}

	if spec.notNull {
		return nil, fmt.Errorf("%w (%s)", ErrNewColumnMustBeNullable, spec.colName)
	}

	if !validMaxLenForType(spec.maxLen, spec.colType) {
		return nil, fmt.Errorf("%w (%s)", ErrLimitedMaxLen, spec.colName)
	}

	_, exists := t.colsByName[spec.colName]
	if exists {
		return nil, fmt.Errorf("%w (%s)", ErrColumnAlreadyExists, spec.colName)
	}

	id := len(t.cols) + 1

	col := &Column{
		id:            uint32(id),
		table:         t,
		colName:       spec.colName,
		colType:       spec.colType,
		maxLen:        spec.maxLen,
		autoIncrement: spec.autoIncrement,
		notNull:       spec.notNull,
	}

	t.cols = append(t.cols, col)
	t.colsByID[col.id] = col
	t.colsByName[col.colName] = col

	return col, nil
}

func (t *Table) renameColumn(oldName, newName string) (*Column, error) {
	if oldName == newName {
		return nil, fmt.Errorf("%w (%s)", ErrSameOldAndNewColumnName, oldName)
	}

	col, exists := t.colsByName[oldName]
	if !exists {
		return nil, fmt.Errorf("%w (%s)", ErrColumnDoesNotExist, oldName)
	}

	_, exists = t.colsByName[newName]
	if exists {
		return nil, fmt.Errorf("%w (%s)", ErrColumnAlreadyExists, newName)
	}

	col.colName = newName

	delete(t.colsByName, oldName)
	t.colsByName[newName] = col

	return col, nil
}

func (c *Column) ID() uint32 {
	return c.id
}

func (c *Column) Name() string {
	return c.colName
}

func (c *Column) Type() SQLValueType {
	return c.colType
}

func (c *Column) MaxLen() int {
	switch c.colType {
	case BooleanType:
		return 1
	case IntegerType:
		return 8
	case TimestampType:
		return 8
	}
	return c.maxLen
}

func (c *Column) IsNullable() bool {
	return !c.notNull
}

func (c *Column) IsAutoIncremental() bool {
	return c.autoIncrement
}

func validMaxLenForType(maxLen int, sqlType SQLValueType) bool {
	switch sqlType {
	case BooleanType:
		return maxLen <= 1
	case IntegerType:
		return maxLen == 0 || maxLen == 8
	case TimestampType:
		return maxLen == 0 || maxLen == 8
	}

	return maxLen >= 0
}

func (c *Catalog) load(sqlPrefix []byte, tx *store.OngoingTx) error {
	dbReaderSpec := store.KeyReaderSpec{
		Prefix:  mapKey(sqlPrefix, catalogDatabasePrefix),
		Filters: []store.FilterFn{store.IgnoreExpired, store.IgnoreDeleted},
	}

	dbReader, err := tx.NewKeyReader(dbReaderSpec)
	if err != nil {
		return err
	}
	defer dbReader.Close()

	for {
		mkey, vref, err := dbReader.Read()
		if err == store.ErrNoMoreEntries {
			break
		}
		if err != nil {
			return err
		}

		id, err := unmapDatabaseID(sqlPrefix, mkey)
		if err != nil {
			return err
		}

		v, err := vref.Resolve()
		if err != nil {
			return err
		}

		db, err := c.newDatabase(id, string(v))
		if err != nil {
			return err
		}

		err = db.loadTables(sqlPrefix, tx)
		if err != nil {
			return err
		}
	}

	return nil
}

func (db *Database) loadTables(sqlPrefix []byte, tx *store.OngoingTx) error {
	dbReaderSpec := store.KeyReaderSpec{
		Prefix:  mapKey(sqlPrefix, catalogTablePrefix, EncodeID(db.id)),
		Filters: []store.FilterFn{store.IgnoreExpired, store.IgnoreDeleted},
	}

	tableReader, err := tx.NewKeyReader(dbReaderSpec)
	if err != nil {
		return err
	}
	defer tableReader.Close()

	for {
		mkey, vref, err := tableReader.Read()
		if err == store.ErrNoMoreEntries {
			break
		}
		if err != nil {
			return err
		}

		dbID, tableID, err := unmapTableID(sqlPrefix, mkey)
		if err != nil {
			return err
		}

		if dbID != db.id {
			return ErrCorruptedData
		}

		colSpecs, err := loadColSpecs(db.id, tableID, tx, sqlPrefix)
		if err != nil {
			return err
		}

		v, err := vref.Resolve()
		if err != nil {
			return err
		}

		table, err := db.newTable(string(v), colSpecs)
		if err != nil {
			return err
		}

		if tableID != table.id {
			return ErrCorruptedData
		}

		err = table.loadIndexes(sqlPrefix, tx)
		if err != nil {
			return err
		}

		if table.autoIncrementPK {
			encMaxPK, err := loadMaxPK(sqlPrefix, tx, table)
			if err == store.ErrNoMoreEntries {
				continue
			}
			if err != nil {
				return err
			}

			if len(encMaxPK) != 9 {
				return ErrCorruptedData
			}

			if encMaxPK[0] != KeyValPrefixNotNull {
				return ErrCorruptedData
			}

			// map to signed integer space
			encMaxPK[1] ^= 0x80

			table.maxPK = int64(binary.BigEndian.Uint64(encMaxPK[1:]))
		}
	}

	return nil
}

func loadMaxPK(sqlPrefix []byte, tx *store.OngoingTx, table *Table) ([]byte, error) {
	pkReaderSpec := store.KeyReaderSpec{
		Prefix:    mapKey(sqlPrefix, PIndexPrefix, EncodeID(table.db.id), EncodeID(table.id), EncodeID(PKIndexID)),
		DescOrder: true,
	}

	pkReader, err := tx.NewKeyReader(pkReaderSpec)
	if err != nil {
		return nil, err
	}
	defer pkReader.Close()

	mkey, _, err := pkReader.Read()
	if err != nil {
		return nil, err
	}

	return unmapIndexEntry(table.primaryIndex, sqlPrefix, mkey)
}

func loadColSpecs(dbID, tableID uint32, tx *store.OngoingTx, sqlPrefix []byte) (specs []*ColSpec, err error) {
	initialKey := mapKey(sqlPrefix, catalogColumnPrefix, EncodeID(dbID), EncodeID(tableID))

	dbReaderSpec := store.KeyReaderSpec{
		Prefix:  initialKey,
		Filters: []store.FilterFn{store.IgnoreExpired, store.IgnoreDeleted},
	}

	colSpecReader, err := tx.NewKeyReader(dbReaderSpec)
	if err != nil {
		return nil, err
	}
	defer colSpecReader.Close()

	specs = make([]*ColSpec, 0)

	for {
		mkey, vref, err := colSpecReader.Read()
		if err == store.ErrNoMoreEntries {
			break
		}
		if err != nil {
			return nil, err
		}

		mdbID, mtableID, colID, colType, err := unmapColSpec(sqlPrefix, mkey)
		if err != nil {
			return nil, err
		}

		if dbID != mdbID || tableID != mtableID {
			return nil, ErrCorruptedData
		}

		v, err := vref.Resolve()
		if err != nil {
			return nil, err
		}
		if len(v) < 6 {
			return nil, ErrCorruptedData
		}

		spec := &ColSpec{
			colName:       string(v[5:]),
			colType:       colType,
			maxLen:        int(binary.BigEndian.Uint32(v[1:])),
			autoIncrement: v[0]&autoIncrementFlag != 0,
			notNull:       v[0]&nullableFlag != 0,
		}

		specs = append(specs, spec)

		if int(colID) != len(specs) {
			return nil, ErrCorruptedData
		}
	}

	return
}

func (table *Table) loadIndexes(sqlPrefix []byte, tx *store.OngoingTx) error {
	initialKey := mapKey(sqlPrefix, catalogIndexPrefix, EncodeID(table.db.id), EncodeID(table.id))

	idxReaderSpec := store.KeyReaderSpec{
		Prefix:  initialKey,
		Filters: []store.FilterFn{store.IgnoreExpired, store.IgnoreDeleted},
	}

	idxSpecReader, err := tx.NewKeyReader(idxReaderSpec)
	if err != nil {
		return err
	}
	defer idxSpecReader.Close()

	for {
		mkey, vref, err := idxSpecReader.Read()
		if err == store.ErrNoMoreEntries {
			break
		}
		if err != nil {
			return err
		}

		dbID, tableID, indexID, err := unmapIndex(sqlPrefix, mkey)
		if err != nil {
			return err
		}

		if table.id != tableID || table.db.id != dbID {
			return ErrCorruptedData
		}

		v, err := vref.Resolve()
		if err != nil {
			return err
		}

		// v={unique {colID1}(ASC|DESC)...{colIDN}(ASC|DESC)}
		colSpecLen := EncIDLen + 1

		if len(v) < 1+colSpecLen || len(v)%colSpecLen != 1 {
			return ErrCorruptedData
		}

		var colIDs []uint32

		for i := 1; i < len(v); i += colSpecLen {
			colID := binary.BigEndian.Uint32(v[i:])

			// TODO: currently only ASC order is supported
			if v[i+EncIDLen] != 0 {
				return ErrCorruptedData
			}

			colIDs = append(colIDs, colID)
		}

		index, err := table.newIndex(v[0] > 0, colIDs)
		if err != nil {
			return err
		}

		if indexID != index.id {
			return ErrCorruptedData
		}
	}

	return nil
}

func trimPrefix(prefix, mkey []byte, mappingPrefix []byte) ([]byte, error) {
	if len(prefix)+len(mappingPrefix) > len(mkey) ||
		!bytes.Equal(prefix, mkey[:len(prefix)]) ||
		!bytes.Equal(mappingPrefix, mkey[len(prefix):len(prefix)+len(mappingPrefix)]) {
		return nil, ErrIllegalMappedKey
	}

	return mkey[len(prefix)+len(mappingPrefix):], nil
}

func unmapDatabaseID(prefix, mkey []byte) (dbID uint32, err error) {
	encID, err := trimPrefix(prefix, mkey, []byte(catalogDatabasePrefix))
	if err != nil {
		return 0, err
	}

	if len(encID) != EncIDLen {
		return 0, ErrCorruptedData
	}

	return binary.BigEndian.Uint32(encID), nil
}

func unmapTableID(prefix, mkey []byte) (dbID, tableID uint32, err error) {
	encID, err := trimPrefix(prefix, mkey, []byte(catalogTablePrefix))
	if err != nil {
		return 0, 0, err
	}

	if len(encID) != EncIDLen*2 {
		return 0, 0, ErrCorruptedData
	}

	dbID = binary.BigEndian.Uint32(encID)
	tableID = binary.BigEndian.Uint32(encID[EncIDLen:])

	return
}

func unmapColSpec(prefix, mkey []byte) (dbID, tableID, colID uint32, colType SQLValueType, err error) {
	encID, err := trimPrefix(prefix, mkey, []byte(catalogColumnPrefix))
	if err != nil {
		return 0, 0, 0, "", err
	}

	if len(encID) < EncIDLen*3 {
		return 0, 0, 0, "", ErrCorruptedData
	}

	dbID = binary.BigEndian.Uint32(encID)
	tableID = binary.BigEndian.Uint32(encID[EncIDLen:])
	colID = binary.BigEndian.Uint32(encID[2*EncIDLen:])

	colType, err = asType(string(encID[EncIDLen*3:]))
	if err != nil {
		return 0, 0, 0, "", ErrCorruptedData
	}

	return
}

func asType(t string) (SQLValueType, error) {
	if t == IntegerType ||
		t == BooleanType ||
		t == VarcharType ||
		t == BLOBType ||
		t == TimestampType {
		return t, nil
	}

	return t, ErrCorruptedData
}

func unmapIndex(sqlPrefix, mkey []byte) (dbID, tableID, indexID uint32, err error) {
	encID, err := trimPrefix(sqlPrefix, mkey, []byte(catalogIndexPrefix))
	if err != nil {
		return 0, 0, 0, err
	}

	if len(encID) != EncIDLen*3 {
		return 0, 0, 0, ErrCorruptedData
	}

	dbID = binary.BigEndian.Uint32(encID)
	tableID = binary.BigEndian.Uint32(encID[EncIDLen:])
	indexID = binary.BigEndian.Uint32(encID[EncIDLen*2:])

	return
}

func unmapIndexEntry(index *Index, sqlPrefix, mkey []byte) (encPKVals []byte, err error) {
	if index == nil {
		return nil, ErrIllegalArguments
	}

	enc, err := trimPrefix(sqlPrefix, mkey, []byte(index.prefix()))
	if err != nil {
		return nil, ErrCorruptedData
	}

	if len(enc) <= EncIDLen*3 {
		return nil, ErrCorruptedData
	}

	off := 0

	dbID := binary.BigEndian.Uint32(enc[off:])
	off += EncIDLen

	tableID := binary.BigEndian.Uint32(enc[off:])
	off += EncIDLen

	indexID := binary.BigEndian.Uint32(enc[off:])
	off += EncIDLen

	if dbID != index.table.db.id || tableID != index.table.id || indexID != index.id {
		return nil, ErrCorruptedData
	}

	if !index.IsPrimary() {
		//read index values
		for _, col := range index.cols {
			if enc[off] == KeyValPrefixNull {
				off += 1
				continue
			}
			if enc[off] != KeyValPrefixNotNull {
				return nil, ErrCorruptedData
			}
			off += 1

			maxLen := col.MaxLen()
			if variableSized(col.colType) {
				maxLen += EncLenLen
			}
			if len(enc)-off < maxLen {
				return nil, ErrCorruptedData
			}

			off += maxLen
		}
	}

	//PK cannot be nil
	if len(enc)-off < 1 {
		return nil, ErrCorruptedData
	}

	return enc[off:], nil
}

func variableSized(sqlType SQLValueType) bool {
	return sqlType == VarcharType || sqlType == BLOBType
}

func mapKey(prefix []byte, mappingPrefix string, encValues ...[]byte) []byte {
	return MapKey(prefix, mappingPrefix, encValues...)
}

func MapKey(prefix []byte, mappingPrefix string, encValues ...[]byte) []byte {
	mkeyLen := len(prefix) + len(mappingPrefix)

	for _, ev := range encValues {
		mkeyLen += len(ev)
	}

	mkey := make([]byte, mkeyLen)

	off := 0

	copy(mkey, prefix)
	off += len(prefix)

	copy(mkey[off:], []byte(mappingPrefix))
	off += len(mappingPrefix)

	for _, ev := range encValues {
		copy(mkey[off:], ev)
		off += len(ev)
	}

	return mkey
}

func EncodeID(id uint32) []byte {
	var encID [EncIDLen]byte
	binary.BigEndian.PutUint32(encID[:], id)
	return encID[:]
}

func EncodeValue(val interface{}, colType SQLValueType, maxLen int) ([]byte, error) {
	switch colType {
	case VarcharType:
		{
			strVal, ok := val.(string)
			if !ok {
				return nil, fmt.Errorf(
					"value is not a string: %w", ErrInvalidValue,
				)
			}

			if maxLen > 0 && len(strVal) > maxLen {
				return nil, ErrMaxLengthExceeded
			}

			// len(v) + v
			encv := make([]byte, EncLenLen+len(strVal))
			binary.BigEndian.PutUint32(encv[:], uint32(len(strVal)))
			copy(encv[EncLenLen:], []byte(strVal))

			return encv, nil
		}
	case IntegerType:
		{
			intVal, ok := val.(int64)
			if !ok {
				return nil, fmt.Errorf(
					"value is not an integer: %w", ErrInvalidValue,
				)
			}

			// map to unsigned integer space
			// len(v) + v
			var encv [EncLenLen + 8]byte
			binary.BigEndian.PutUint32(encv[:], uint32(8))
			binary.BigEndian.PutUint64(encv[EncLenLen:], uint64(intVal))

			return encv[:], nil
		}
	case BooleanType:
		{
			boolVal, ok := val.(bool)
			if !ok {
				return nil, fmt.Errorf(
					"value is not a boolean: %w", ErrInvalidValue,
				)
			}

			// len(v) + v
			var encv [EncLenLen + 1]byte
			binary.BigEndian.PutUint32(encv[:], uint32(1))
			if boolVal {
				encv[EncLenLen] = 1
			}

			return encv[:], nil
		}
	case BLOBType:
		{
			var blobVal []byte

			if val != nil {
				v, ok := val.([]byte)
				if !ok {
					return nil, fmt.Errorf(
						"value is not a blob: %w", ErrInvalidValue,
					)
				}
				blobVal = v
			}

			if maxLen > 0 && len(blobVal) > maxLen {
				return nil, ErrMaxLengthExceeded
			}

			// len(v) + v
			encv := make([]byte, EncLenLen+len(blobVal))
			binary.BigEndian.PutUint32(encv[:], uint32(len(blobVal)))
			copy(encv[EncLenLen:], blobVal)

			return encv[:], nil
		}
	case TimestampType:
		{
			timeVal, ok := val.(time.Time)
			if !ok {
				return nil, fmt.Errorf(
					"value is not a timestamp: %w", ErrInvalidValue,
				)
			}

			// len(v) + v
			var encv [EncLenLen + 8]byte
			binary.BigEndian.PutUint32(encv[:], uint32(8))
			binary.BigEndian.PutUint64(encv[EncLenLen:], uint64(TimeToInt64(timeVal)))

			return encv[:], nil
		}
	}

	return nil, ErrInvalidValue
}

const (
	KeyValPrefixNull       byte = 0x20
	KeyValPrefixNotNull    byte = 0x80
	KeyValPrefixUpperBound byte = 0xFF
)

func EncodeAsKey(val interface{}, colType SQLValueType, maxLen int) ([]byte, error) {
	if maxLen <= 0 {
		return nil, ErrInvalidValue
	}
	if maxLen > maxKeyLen {
		return nil, ErrMaxKeyLengthExceeded
	}

	if val == nil {
		return []byte{KeyValPrefixNull}, nil
	}

	switch colType {
	case VarcharType:
		{
			strVal, ok := val.(string)
			if !ok {
				return nil, fmt.Errorf(
					"value is not a string: %w", ErrInvalidValue,
				)
			}

			if len(strVal) > maxLen {
				return nil, ErrMaxLengthExceeded
			}

			// notnull + value + padding + len(value)
			encv := make([]byte, 1+maxLen+EncLenLen)
			encv[0] = KeyValPrefixNotNull
			copy(encv[1:], []byte(strVal))
			binary.BigEndian.PutUint32(encv[len(encv)-EncLenLen:], uint32(len(strVal)))

			return encv, nil
		}
	case IntegerType:
		{
			if maxLen != 8 {
				return nil, ErrCorruptedData
			}

			intVal, ok := val.(int64)
			if !ok {
				return nil, fmt.Errorf(
					"value is not an integer: %w", ErrInvalidValue,
				)
			}

			// v
			var encv [9]byte
			encv[0] = KeyValPrefixNotNull
			binary.BigEndian.PutUint64(encv[1:], uint64(intVal))
			// map to unsigned integer space for lexical sorting order
			encv[1] ^= 0x80

			return encv[:], nil
		}
	case BooleanType:
		{
			if maxLen != 1 {
				return nil, ErrCorruptedData
			}

			boolVal, ok := val.(bool)
			if !ok {
				return nil, fmt.Errorf(
					"value is not a boolean: %w", ErrInvalidValue,
				)
			}

			// v
			var encv [2]byte
			encv[0] = KeyValPrefixNotNull
			if boolVal {
				encv[1] = 1
			}

			return encv[:], nil
		}
	case BLOBType:
		{
			blobVal, ok := val.([]byte)
			if !ok {
				return nil, fmt.Errorf(
					"value is not a blob: %w", ErrInvalidValue,
				)
			}

			if len(blobVal) > maxLen {
				return nil, ErrMaxLengthExceeded
			}

			// notnull + value + padding + len(value)
			encv := make([]byte, 1+maxLen+EncLenLen)
			encv[0] = KeyValPrefixNotNull
			copy(encv[1:], []byte(blobVal))
			binary.BigEndian.PutUint32(encv[len(encv)-EncLenLen:], uint32(len(blobVal)))

			return encv, nil
		}
	case TimestampType:
		{
			if maxLen != 8 {
				return nil, ErrCorruptedData
			}

			timeVal, ok := val.(time.Time)
			if !ok {
				return nil, fmt.Errorf(
					"value is not a timestamp: %w", ErrInvalidValue,
				)
			}

			// v
			var encv [9]byte
			encv[0] = KeyValPrefixNotNull
			binary.BigEndian.PutUint64(encv[1:], uint64(timeVal.UnixNano()))
			// map to unsigned integer space for lexical sorting order
			encv[1] ^= 0x80

			return encv[:], nil
		}

	}

	return nil, ErrInvalidValue
}

func DecodeValue(b []byte, colType SQLValueType) (TypedValue, int, error) {
	if len(b) < EncLenLen {
		return nil, 0, ErrCorruptedData
	}

	vlen := int(binary.BigEndian.Uint32(b[:]))
	voff := EncLenLen

	if vlen < 0 || len(b) < voff+vlen {
		return nil, 0, ErrCorruptedData
	}

	switch colType {
	case VarcharType:
		{
			v := string(b[voff : voff+vlen])
			voff += vlen

			return &Varchar{val: v}, voff, nil
		}
	case IntegerType:
		{
			if vlen != 8 {
				return nil, 0, ErrCorruptedData
			}

			v := binary.BigEndian.Uint64(b[voff:])
			voff += vlen

			return &Number{val: int64(v)}, voff, nil
		}
	case BooleanType:
		{
			if vlen != 1 {
				return nil, 0, ErrCorruptedData
			}

			v := b[voff] == 1
			voff += 1

			return &Bool{val: v}, voff, nil
		}
	case BLOBType:
		{
			v := b[voff : voff+vlen]
			voff += vlen

			return &Blob{val: v}, voff, nil
		}
	case TimestampType:
		{
			if vlen != 8 {
				return nil, 0, ErrCorruptedData
			}

			v := binary.BigEndian.Uint64(b[voff:])
			voff += vlen

			return &Timestamp{val: TimeFromInt64(int64(v))}, voff, nil
		}
	}

	return nil, 0, ErrCorruptedData
}
