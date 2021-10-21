// Copyright (C) 2019-2020 Zilliz. All rights reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License"); you may not use this file except in compliance
// with the License. You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software distributed under the License
// is distributed on an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express
// or implied. See the License for the specific language governing permissions and limitations under the License.

package storage

import (
	"errors"
	"sync/atomic"

	"github.com/milvus-io/milvus/internal/rootcoord"
)

var (
	// ErrNoMoreRecord is the error that the iterator does not have next record
	ErrNoMoreRecord = errors.New("no more record")
	// ErrDisposed is the error that the iterator is disposed
	ErrDisposed = errors.New("iterator is disposed")
)

// Iterator is the iterator interface
type Iterator interface {
	// HasNext returns true if the iterator have unread record
	HasNext() bool
	// Next returns the next record
	Next() (interface{}, error)
	// Dispose disposes the iterator
	Dispose()
}

// Value is the return value of HasNext
type Value struct {
	id        int64
	timestamp int64
	isDeleted bool
	value     interface{}
}

// InsertBinlogIterator is the iterator of binlog
type InsertBinlogIterator struct {
	dispose int32 // 0: false, 1: true
	data    *InsertData
	fieldID int64
	pos     int
}

// NewInsertBinlogIterator creates a new iterator
func NewInsertBinlogIterator(blobs []*Blob) (*InsertBinlogIterator, error) {
	// TODO: load part of file to read records other than loading all content
	reader := NewInsertCodec(nil)

	_, _, serData, err := reader.Deserialize(blobs)
	defer reader.Close()

	if err != nil {
		return nil, err
	}

	return &InsertBinlogIterator{data: serData}, nil
}

// HasNext returns true if the iterator have unread record
func (itr *InsertBinlogIterator) HasNext() bool {
	return !itr.isDisposed() && itr.hasNext()
}

// Next returns the next record
func (itr *InsertBinlogIterator) Next() (interface{}, error) {
	if itr.isDisposed() {
		return nil, ErrDisposed
	}

	if !itr.hasNext() {
		return nil, ErrNoMoreRecord
	}

	m := make(map[FieldID]interface{})
	for fieldID, fieldData := range itr.data.Data {
		m[fieldID] = fieldData.Get(itr.pos)
	}

	v := &Value{
		id:        itr.data.Data[rootcoord.RowIDField].Get(itr.pos).(int64),
		timestamp: itr.data.Data[rootcoord.TimeStampField].Get(itr.pos).(int64),
		isDeleted: false,
		value:     m,
	}
	itr.pos++
	return v, nil
}

// Dispose disposes the iterator
func (itr *InsertBinlogIterator) Dispose() {
	atomic.CompareAndSwapInt32(&itr.dispose, 0, 1)
}

func (itr *InsertBinlogIterator) hasNext() bool {
	_, ok := itr.data.Data[rootcoord.RowIDField]
	if !ok {
		return false
	}
	return itr.pos < itr.data.Data[rootcoord.RowIDField].Length()
}

func (itr *InsertBinlogIterator) isDisposed() bool {
	return atomic.LoadInt32(&itr.dispose) == 1
}

/*
type DeltalogIterator struct {
	dispose int32
	values  []*Value
	pos     int
}

func NewDeltalogIterator(blob *Blob) (*DeltalogIterator, error) {
	deltaCodec := NewDeleteCodec(nil)
	_, _, serData, err := deltaCodec.Deserialize(blob)
	if err != nil {
		return nil, err
	}

	values := make([]*Value, 0, len(serData.Data))
	for pkstr, ts := range serData.Data {
		pk, err := strconv.ParseInt(pkstr, 10, 64)
		if err != nil {
			return nil, err
		}
		values = append(values, &Value{pk, ts, true, nil})
	}

	sort.Slice(values, func(i, j int) bool { return values[i].id < values[j].id })

	return &DeltalogIterator{values: values}, nil
}

// HasNext returns true if the iterator have unread record
func (itr *DeltalogIterator) HasNext() bool {
	return !itr.isDisposed() && itr.hasNext()
}

// Next returns the next record
func (itr *DeltalogIterator) Next() (interface{}, error) {
	if itr.isDisposed() {
		return nil, ErrDisposed
	}

	if !itr.hasNext() {
		return nil, ErrNoMoreRecord
	}

	tmp := itr.values[itr.pos]
	itr.pos++
	return tmp, nil
}

// Dispose disposes the iterator
func (itr *DeltalogIterator) Dispose() {
	atomic.CompareAndSwapInt32(&itr.dispose, 0, 1)
}

func (itr *DeltalogIterator) hasNext() bool {
	return itr.pos < len(itr.values)
}

func (itr *DeltalogIterator) isDisposed() bool {
	return atomic.LoadInt32(&itr.dispose) == 1
}

*/

type MergeIterator struct {
	disposed   int32
	pos        int
	iteraotrs  []Iterator
	tmpRecords []*Value
	nextRecord *Value
}

func NewMergeIterator(iterators []Iterator) *MergeIterator {
	return &MergeIterator{
		iteraotrs:  iterators,
		tmpRecords: make([]*Value, len(iterators)),
	}
}

// HasNext returns true if the iterator have unread record
func (itr *MergeIterator) HasNext() bool {
	return !itr.isDisposed() && itr.hasNext()
}

// Next returns the next record
func (itr *MergeIterator) Next() (interface{}, error) {
	if itr.isDisposed() {
		return nil, ErrDisposed
	}

	if !itr.hasNext() {
		return nil, ErrNoMoreRecord
	}

	tmpRecord := itr.nextRecord
	itr.nextRecord = nil
	return tmpRecord, nil
}

// Dispose disposes the iterator
func (itr *MergeIterator) Dispose() {
	if itr.isDisposed() {
		return
	}

	for _, tmpItr := range itr.iteraotrs {
		if tmpItr != nil {
			tmpItr.Dispose()
		}
	}
	atomic.CompareAndSwapInt32(&itr.disposed, 0, 1)
}

func (itr *MergeIterator) isDisposed() bool {
	return atomic.LoadInt32(&itr.disposed) == 1
}

func (itr *MergeIterator) hasNext() bool {
	if itr.nextRecord != nil {
		return true
	}

	var minRecord *Value
	var minPos int
	for i, tmpRecord := range itr.tmpRecords {
		if tmpRecord == nil {
			if itr.iteraotrs[i] != nil && itr.iteraotrs[i].HasNext() {
				next, _ := itr.iteraotrs[i].Next()
				itr.tmpRecords[i] = next.(*Value)
				tmpRecord = itr.tmpRecords[i]
			}
		}
		if tmpRecord == nil {
			continue
		}
		if minRecord == nil || tmpRecord.id < minRecord.id {
			minRecord = tmpRecord
			minPos = i
		}
	}

	if minRecord == nil {
		// all iterators have no more records
		return false
	}

	itr.tmpRecords[minPos] = nil
	itr.nextRecord = minRecord
	return true
}

/*
func NewInsertlogMergeIterator(blobs [][]*Blob) (*MergeIterator, error) {
	iterators := make([]Iterator, 0, len(blobs))
	for _, fieldBlobs := range blobs {
		itr, err := NewInsertBinlogIterator(fieldBlobs)
		if err != nil {
			return nil, err
		}
		iterators = append(iterators, itr)
	}

	return NewMergeIterator(iterators), nil
}

func NewDeltalogMergeIterator(blobs []*Blob) (*MergeIterator, error) {
	iterators := make([]Iterator, 0, len(blobs))
	for _, blob := range blobs {
		itr, err := NewDeltalogIterator(blob)
		if err != nil {
			return nil, err
		}
		iterators = append(iterators, itr)
	}
	return NewMergeIterator(iterators), nil
}

type MergeSingleSegmentIterator struct {
	disposed        int32
	insertItr       Iterator
	deltaItr        Iterator
	timetravel      int64
	nextRecord      *Value
	insertTmpRecord *Value
	deltaTmpRecord  *Value
}

func NewMergeSingleSegmentIterator(insertBlobs [][]*Blob, deltaBlobs []*Blob, timetravel int64) (*MergeSingleSegmentIterator, error) {
	insertMergeItr, err := NewInsertlogMergeIterator(insertBlobs)
	if err != nil {
		return nil, err
	}

	deltaMergeItr, err := NewDeltalogMergeIterator(deltaBlobs)
	if err != nil {
		return nil, err
	}
	return &MergeSingleSegmentIterator{
		insertItr:  insertMergeItr,
		deltaItr:   deltaMergeItr,
		timetravel: timetravel,
	}, nil
}

// HasNext returns true if the iterator have unread record
func (itr *MergeSingleSegmentIterator) HasNext() bool {
	return !itr.isDisposed() && itr.hasNext()
}

// Next returns the next record
func (itr *MergeSingleSegmentIterator) Next() (interface{}, error) {
	if itr.isDisposed() {
		return nil, ErrDisposed
	}
	if !itr.hasNext() {
		return nil, ErrNoMoreRecord
	}

	tmp := itr.nextRecord
	itr.nextRecord = nil
	return tmp, nil
}

// Dispose disposes the iterator
func (itr *MergeSingleSegmentIterator) Dispose() {
	if itr.isDisposed() {
		return
	}

	if itr.insertItr != nil {
		itr.insertItr.Dispose()
	}
	if itr.deltaItr != nil {
		itr.deltaItr.Dispose()
	}

	atomic.CompareAndSwapInt32(&itr.disposed, 0, 1)
}

func (itr *MergeSingleSegmentIterator) isDisposed() bool {
	return atomic.LoadInt32(&itr.disposed) == 1
}

func (itr *MergeSingleSegmentIterator) hasNext() bool {
	if itr.nextRecord != nil {
		return true
	}

	for {
		if itr.insertTmpRecord == nil && itr.insertItr.HasNext() {
			r, _ := itr.insertItr.Next()
			itr.insertTmpRecord = r.(*Value)
		}

		if itr.deltaTmpRecord == nil && itr.deltaItr.HasNext() {
			r, _ := itr.deltaItr.Next()
			itr.deltaTmpRecord = r.(*Value)
		}

		if itr.insertTmpRecord == nil && itr.deltaTmpRecord == nil {
			return false
		} else if itr.insertTmpRecord == nil {
			itr.nextRecord = itr.deltaTmpRecord
			itr.deltaTmpRecord = nil
			return true
		} else if itr.deltaTmpRecord == nil {
			itr.nextRecord = itr.insertTmpRecord
			itr.insertTmpRecord = nil
			return true
		} else {
			// merge records
			if itr.insertTmpRecord.timestamp >= itr.timetravel {
				itr.nextRecord = itr.insertTmpRecord
				itr.insertTmpRecord = nil
				return true
			}
			if itr.deltaTmpRecord.timestamp >= itr.timetravel {
				itr.nextRecord = itr.deltaTmpRecord
				itr.deltaTmpRecord = nil
				return true
			}

			if itr.insertTmpRecord.id < itr.deltaTmpRecord.id {
				itr.nextRecord = itr.insertTmpRecord
				itr.insertTmpRecord = nil
				return true
			} else if itr.insertTmpRecord.id > itr.deltaTmpRecord.id {
				itr.deltaTmpRecord = nil
				continue
			} else if itr.insertTmpRecord.id == itr.deltaTmpRecord.id {
				if itr.insertTmpRecord.timestamp <= itr.deltaTmpRecord.timestamp {
					itr.insertTmpRecord = nil
					continue
				} else {
					itr.deltaTmpRecord = nil
					continue
				}
			}
		}

	}
}
*/