/*
Copyright 2022 CodeNotary, Inc. All rights reserved.

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

package tokenservice

import (
	"encoding/binary"
	"errors"
	"io/ioutil"
	"os"
	"strings"
	"sync"
)

type file struct {
	sync.Mutex
	tokenAbsPath string
}

//NewFileTokenService ...
func NewFileTokenService() *file {
	return &file{
		tokenAbsPath: "token",
	}
}

func (ts *file) GetToken() (string, error) {
	ts.Lock()
	defer ts.Unlock()
	_, token, err := ts.parseContent()
	if err != nil {
		return "", err
	}
	return token, nil
}

//SetToken ...
func (ts *file) SetToken(database string, token string) error {
	ts.Lock()
	defer ts.Unlock()
	if token == "" {
		return ErrEmptyTokenProvided
	}
	return ioutil.WriteFile(ts.tokenAbsPath, BuildToken(database, token), 0644)
}

func BuildToken(database string, token string) []byte {
	dbsl := uint64(len(database))
	dbnl := len(database)
	tl := len(token)
	lendbs := binary.Size(dbsl)
	var cnt = make([]byte, lendbs+dbnl+tl)
	binary.BigEndian.PutUint64(cnt, dbsl)
	copy(cnt[lendbs:], database)
	copy(cnt[lendbs+dbnl:], token)
	return cnt
}

func (ts *file) DeleteToken() error {
	ts.Lock()
	defer ts.Unlock()
	return os.Remove(ts.tokenAbsPath)
}

//IsTokenPresent ...
func (ts *file) IsTokenPresent() (bool, error) {
	ts.Lock()
	defer ts.Unlock()
	_, _, err := ts.parseContent()
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (ts *file) GetDatabase() (string, error) {
	ts.Lock()
	defer ts.Unlock()
	dbname, _, err := ts.parseContent()
	return dbname, err
}

func (ts *file) parseContent() (string, string, error) {
	contentBytes, err := ioutil.ReadFile(ts.tokenAbsPath)
	if err != nil {
		return "", "", err
	}
	content := string(contentBytes)

	if len(content) <= 8 {
		return "", "", ErrTokenContentNotPresent
	}
	// token prefix is hardcoded into library. Please modify in case of changes in paseto library
	if strings.HasPrefix(content, "v2.public.") {
		return "", "", errors.New("old token format. Please remove old token located in your default home dir")
	}
	dbNameLen := make([]byte, 8)
	copy(dbNameLen, content[:8])
	dbNameLenUint64 := binary.BigEndian.Uint64(dbNameLen)
	if dbNameLenUint64 > uint64(len(content))-8 {
		return "", "", errors.New("invalid token format")
	}
	databasename := make([]byte, dbNameLenUint64)
	copy(databasename, content[8:8+dbNameLenUint64])

	token := make([]byte, uint64(len(content))-8-dbNameLenUint64)
	copy(token, content[8+dbNameLenUint64:])

	return string(databasename), string(token), nil
}

// WithTokenFileAbsPath ...
func (ts *file) WithTokenFileAbsPath(tfn string) *file {
	ts.tokenAbsPath = tfn
	return ts
}
