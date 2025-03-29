package memtable

import (
	"encoding/binary"
	"math/rand"
	"os"
	"sync"
	"time"
)

const (
	FILEPATH = "sstable_"
)

type Memtable struct {
	skiplist *SkipList
	mu       sync.RWMutex
}

type SSTable struct {
	file  *os.File
	index map[string]int64
}

type SSTableHeader struct {
	MagicNumber      uint32
	DataBlockOffset  uint64
	IndexBlockOffset uint64
}

type DataBlockEntry struct {
	KeyLength   uint64
	Key         string
	ValueLength uint64
	Value       string
}

type IndexBlockEntry struct {
	KeyLength uint64
	Key       string
	Offset    int64
}

func NewMemtable() *Memtable {
	return &Memtable{
		skiplist: newSkipList(),
	}
}

func (m *Memtable) Put(key string, value string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.skiplist.insert(key, value)
}

func (m *Memtable) Get(key string) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	m.skiplist.search(key)
}

func (m *Memtable) Delete(key string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.skiplist.insert(key, "DEL")
}

type SkipListNode struct {
	Key       string
	Value     string
	Tombstone bool
	Down      *SkipListNode
	Right     *SkipListNode
}

type SkipList struct {
	Head  *SkipListNode
	Level int
}

func newSkipList() *SkipList {
	return &SkipList{
		Head:  &SkipListNode{Key: "", Value: "", Right: nil, Down: nil},
		Level: 1,
	}
}

func (s *SkipList) search(key string) (*SkipListNode, bool) {
	if s == nil {
		return nil, false
	}

	currList := s.Head
	for currList != nil {

		for currList.Right != nil && currList.Right.Key < key {
			currList = currList.Right
		}

		if currList.Right != nil && currList.Right.Key == key {
			if currList.Right.Tombstone {
				return nil, false
			}
			return currList.Right, true
		}
		currList = currList.Down
	}
	return nil, false
}

func (s *SkipList) insert(key string, value string) {

	var stack []*SkipListNode
	curr := s.Head
	var existingNode *SkipListNode
	isFound := false

	for curr != nil {

		for curr.Right != nil && curr.Right.Key < key {
			curr = curr.Right
		}

		if curr.Right != nil && curr.Right.Key == key {
			existingNode = curr.Right
			isFound = true
		}

		stack = append(stack, curr)

		curr = curr.Down
	}

	if isFound {
		if value == "DEL" {
			existingNode.Tombstone = true
		} else {
			existingNode.Value = value
			existingNode.Tombstone = false
		}
		return
	}

	nextLevelNode := (*SkipListNode)(nil)
	isInserted := true
	level := 0

	rand.Seed(time.Now().UnixNano())

	for isInserted && level < len(stack) {
		prev := stack[len(stack)-1-level]
		stack = stack[:len(stack)-1]

		newNode := &SkipListNode{
			Key:   key,
			Value: value,
			Right: prev.Right,
			Down:  nextLevelNode,
		}

		prev.Right = newNode
		nextLevelNode = newNode

		if rand.Intn(2) == 1 {
			isInserted = true
		} else {
			isInserted = false
		}

		level++
	}

	if isInserted {
		newHead := &SkipListNode{Right: nil, Down: s.Head}
		s.Head = newHead
		s.Level++
	}
}

func (s *SkipList) getLastLevelList() *SkipListNode {
	curr := s.Head
	for curr.Down != nil {
		curr = curr.Down
	}
	return curr
}

func (m *Memtable) FlushToSSTable(s *SSTable) {
	m.mu.Lock()
	defer m.mu.Unlock()

	it := m.skiplist.getLastLevelList()

	var dataBlockEntries []*DataBlockEntry

	for it.Right != nil {
		key := it.Key
		value := it.Value
		keyLen := uint64(len(key))
		valueLen := uint64(len(value))

		if value != "DEL" {
			dataBlockEntries = append(dataBlockEntries,
				&DataBlockEntry{KeyLength: keyLen, Key: key, ValueLength: valueLen, Value: value})
		}
	}

	s.WriteToSSTable(dataBlockEntries)

	m.skiplist = newSkipList()
}

func (s *SSTable) WriteToSSTable(datablockEntry []*DataBlockEntry) error {

	file, err := os.Create(FILEPATH + time.Now().Format(time.RFC3339) + ".db")
	if err != nil {
		return err
	}

	defer file.Close()

	var indexBlockEntries []IndexBlockEntry
	dataBlockOffset := uint64(12)

	for _, entry := range datablockEntry {
		offset := dataBlockOffset

		indexBlockEntries = append(indexBlockEntries, IndexBlockEntry{KeyLength: entry.KeyLength, Key: entry.Key, Offset: offset})

		keyLen := entry.KeyLength
		valueLen := entry.ValueLength

		binary.Write(file, binary.LittleEndian, keyLen)
		file.WriteString(entry.Key)
		binary.Write(file, binary.LittleEndian, valueLen)
		file.WriteString(entry.Value)

		dataBlockOffset += 4 + keyLen + valueLen
	}

	indexBlockOffset := dataBlockOffset

	for _, entry := range indexBlockEntries {
		binary.Write(file, binary.LittleEndian, entry.KeyLength)
		file.WriteString(entry.Key)
		binary.Write(file, binary.LittleEndian, entry.Offset)
	}

	file.Seek(0, 0)
	header := SSTableHeader{
		MagicNumber:      123123,
		DataBlockOffset:  12,
		IndexBlockOffset: indexBlockOffset,
	}

	s.file = file

	binary.Write(file, binary.LittleEndian, header)
	return nil
}

func (s *SSTable) LoadSSTableIndex(filename string) {

	file, err := os.Open(filename)
	if err != nil {
		return
	}

	defer file.Close()
	header := SSTableHeader{}

	binary.Read(file, binary.LittleEndian, &header)

	indexBlockOffset := header.IndexBlockOffset
	file.Seek(int64(indexBlockOffset), 0)

	s.index = make(map[string]int64)
	for {
		var entry IndexBlockEntry

		err = binary.Read(file, binary.LittleEndian, &entry.KeyLength)
		if err != nil {
			break
		}

		keyBytes := make([]byte, entry.KeyLength)
		_, err = file.Read(keyBytes)
		if err != nil {
			break
		}
		entry.Key = string(keyBytes)

		err = binary.Read(file, binary.LittleEndian, &entry.Offset)
		if err != nil {
			break
		}
		s.index[entry.Key] = entry.Offset
	}
}

func (s *SSTable) Lookup(key string) (string, bool) {
	offset, exists := s.index[key]
	if !exists {
		return "", false
	}

	return s.readDataBlock(offset)
}

func (s *SSTable) readDataBlock(offset int64) (string, bool) {
	s.file.Seek(offset, 0)

	var keyLen uint64
	var valueLen uint64
	binary.Read(s.file, binary.LittleEndian, &keyLen)
	keyBytes := make([]byte, keyLen)
	s.file.Read(keyBytes)

	binary.Read(s.file, binary.LittleEndian, &valueLen)
	valueBytes := make([]byte, valueLen)
	s.file.Read(valueBytes)

	return string(valueBytes), true
}
