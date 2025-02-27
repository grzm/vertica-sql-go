package vertigo

// Copyright (c) 2019 Micro Focus or one of its affiliates.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//    http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//
// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in
// all copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN
// THE SOFTWARE.

import (
	"context"
	"crypto/md5"
	"crypto/sha512"
	"crypto/tls"
	"database/sql/driver"
	"encoding/binary"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/vertica/vertica-sql-go/common"
	"github.com/vertica/vertica-sql-go/logger"
	"github.com/vertica/vertica-sql-go/msgs"
)

const (
	authStateWaiting     int32 = 0
	authStateNegotiating int32 = 1
	authStateFailed      int32 = 2
	authStateOK          int32 = 4
)

var (
	connectionLogger = logger.New("connection")
)

// Connection represents a connection to Vertica
type connection struct {
	conn             net.Conn
	connURL          *url.URL
	parameters       map[string]string
	clientPID        int
	backendPID       uint32
	cancelKey        uint32
	transactionState byte
	authState        int32
	usePreparedStmts bool
	sessionID        string
	serverTZOffset   string
}

// Begin - Begin starts and returns a new transaction. (DEPRECATED)
// From interface: sql.driver.Conn
func (v *connection) Begin() (driver.Tx, error) {
	return nil, nil
}

// BeginTx - Begin starts and returns a new transaction.
// From interface: sql.driver.ConnBeginTx
func (v *connection) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	connectionLogger.Trace("connection.BeginTx()")
	return newTransaction(ctx, v, opts)
}

// Close closes a connection to the Vertica DB. After calling close, you shouldn't use this connection anymore.
// From interface: sql.driver.Conn
func (v *connection) Close() error {
	connectionLogger.Trace("connection.Close()")

	var result error = nil

	if v.conn != nil {
		result = v.conn.Close()
		v.conn = nil
	}

	return result
}

// PrepareContext returns a prepared statement, bound to this connection.
// context is for the preparation of the statement,
// it must not store the context within the statement itself.
// From interface: sql.driver.ConnPrepareContext
func (v *connection) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {

	s, err := newStmt(v, query)

	if err != nil {
		return nil, err
	}

	if v.usePreparedStmts {
		if err = s.prepareAndDescribe(); err != nil {
			return nil, err
		}
	}

	return s, nil
}

// Prepare returns a prepared statement, bound to this connection.
// From interface: sql.driver.Conn
func (v *connection) Prepare(query string) (driver.Stmt, error) {
	return v.PrepareContext(context.Background(), query)
}

// newConnection constructs a new Vertica Connection object based on the connection string.
func newConnection(connString string) (*connection, error) {

	result := &connection{parameters: make(map[string]string), usePreparedStmts: true}

	var err error
	result.connURL, err = url.Parse(connString)

	if err != nil {
		return nil, err
	}

	result.clientPID = os.Getpid()
	result.sessionID = fmt.Sprintf("%s-%s-%d-%d", driverName, driverVersion, result.clientPID, time.Now().Unix())

	// Read the interpolate flag.
	if iFlag := result.connURL.Query().Get("use_prepared_statements"); iFlag != "" {
		result.usePreparedStmts = iFlag == "1"
	}

	sslFlag := strings.ToLower(result.connURL.Query().Get("tlsmode"))
	if sslFlag == "" {
		sslFlag = "none"
	}

	result.conn, err = net.Dial("tcp", result.connURL.Host)

	if err != nil {
		return nil, fmt.Errorf("cannot connect to %s (%s)", result.connURL.Host, err.Error())
	}

	if sslFlag != "none" {
		if err = result.initializeSSL(sslFlag); err != nil {
			return nil, err
		}
	}

	if err = result.handshake(); err != nil {
		return nil, err
	}

	if err = result.initializeSession(); err != nil {
		return nil, err
	}

	return result, nil
}

func (v *connection) recvMessage() (msgs.BackEndMsg, error) {
	msgHeader := make([]byte, 5)

	for {
		var err error

		if err = v.readAll(msgHeader); err != nil {
			return nil, err
		}

		msgSize := int(binary.BigEndian.Uint32(msgHeader[1:]) - 4)

		msgBytes := make([]byte, msgSize)

		if err = v.readAll(msgBytes); err != nil {
			return nil, err
		}

		bem, err := msgs.CreateBackEndMsg(msgHeader[0], msgBytes)

		if err != nil {
			return nil, err
		}

		// Print the message to stdout (for debugging purposes)
		if _, drm := bem.(*msgs.BEDataRowMsg); !drm {
			connectionLogger.Debug("<- " + bem.String())
		}

		return bem, nil
	}
}

func (v *connection) sendMessage(msg msgs.FrontEndMsg) error {
	var result error = nil

	msgBytes, msgTag := msg.Flatten()

	if msgTag != 0 {
		_, result = v.conn.Write([]byte{msgTag})
	}

	if result == nil {
		sizeBytes := make([]byte, 4)
		binary.BigEndian.PutUint32(sizeBytes, uint32(len(msgBytes)+4))

		_, result = v.conn.Write(sizeBytes)

		if result == nil {
			_, result = v.conn.Write(msgBytes)

			if result == nil {
				connectionLogger.Debug("-> " + msg.String())
			}
		}
	}

	if result != nil {
		connectionLogger.Error("-> FAILED SENDING "+msg.String()+": %v", result.Error())
	}

	return result
}

func (v *connection) handshake() error {

	if v.connURL.User == nil {
		return fmt.Errorf("connection string must include a user name")
	}

	userName := v.connURL.User.Username()

	if len(userName) == 0 {
		return fmt.Errorf("connection string must have a non-empty user name")
	}

	if len(v.connURL.Path) <= 1 {
		return fmt.Errorf("connection string must include a database name")
	}

	path := v.connURL.Path[1:]

	msg := &msgs.FEStartupMsg{
		ProtocolVersion: protocolVersion,
		DriverName:      driverName,
		DriverVersion:   driverVersion,
		Username:        userName,
		Database:        path,
		SessionID:       v.sessionID,
		ClientPID:       v.clientPID,
	}

	if err := v.sendMessage(msg); err != nil {
		return err
	}

	for {
		bMsg, err := v.recvMessage()

		if err != nil {
			return err
		}

		switch msg := bMsg.(type) {
		case *msgs.BEErrorMsg:
			return msg.ToErrorType()
		case *msgs.BEReadyForQueryMsg:
			v.transactionState = msg.TransactionState
			return nil
		case *msgs.BEParamStatusMsg:
			v.parameters[msg.ParamName] = msg.ParamValue
		case *msgs.BEKeyDataMsg:
			v.backendPID = msg.BackendPID
			v.cancelKey = msg.CancelKey
		default:
			_, err = v.defaultMessageHandler(msg)
			if err != nil {
				return err
			}
		}
	}
}

// We have to be tricky here since we're inside of a connection, but trying to use interfaces of the
// driver class.
func (v *connection) initializeSession() error {

	stmt, err := newStmt(v, "select now()::timestamptz")

	if err != nil {
		return err
	}

	result, err := stmt.QueryContextRaw(context.Background(), []driver.NamedValue{})

	if err != nil {
		return err
	}

	if len(result.Columns()) != 1 && result.Columns()[1] != "now" || len(result.resultData) != 1 {
		return fmt.Errorf("unable to initialize session; functionality may be unreliable")
	}

	// Peek into the results manually.
	str := string(result.resultData[0].RowData[0])

	if len(str) < 23 {
		return fmt.Errorf("can't get server timezone: %s", str)
	}

	v.serverTZOffset = str[len(str)-3:]

	connectionLogger.Debug("Setting server timezone offset to %s", str[len(str)-3:])

	return nil
}

func (v *connection) defaultMessageHandler(bMsg msgs.BackEndMsg) (bool, error) {

	handled := true

	var err error = nil

	switch msg := bMsg.(type) {
	case *msgs.BEAuthenticationMsg:
		switch msg.Response {
		case common.AuthenticationOK:
			break
		case common.AuthenticationCleartextPassword:
			err = v.authSendPlainTextPassword()
		case common.AuthenticationMD5Password:
			err = v.authSendMD5Password(msg.ExtraAuthData)
		case common.AuthenticationSHA512Password:
			err = v.authSendSHA512Password(msg.ExtraAuthData)
		default:
			handled = false
			err = fmt.Errorf("unsupported authentication scheme: %d", msg.Response)
		}
	case *msgs.BENoticeMsg:
		break
	default:
		handled = false
		err = fmt.Errorf("unhandled message: %v", msg)
	}

	return handled, err
}

func (v *connection) readAll(buf []byte) error {
	readIndex := 0

	for {
		bytesRead, err := v.conn.Read(buf[readIndex:])

		if err != nil {
			return err
		}

		readIndex += bytesRead

		if readIndex == len(buf) {
			return nil
		}
	}
}

func (v *connection) initializeSSL(sslFlag string) error {
	v.sendMessage(&msgs.FESSLMsg{})

	buf := make([]byte, 1)

	err := v.readAll(buf)

	if err != nil {
		return err
	}

	if buf[0] == 'N' {
		return fmt.Errorf("SSL/TLS is not enabled on this server")
	}

	if buf[0] != 'S' {
		return fmt.Errorf("SSL/TLS probe gave unknown response: %c", buf[0])
	}

	switch sslFlag {
	case "server":
		connectionLogger.Info("enabling SSL/TLS server mode")
		v.conn = tls.Client(v.conn, &tls.Config{InsecureSkipVerify: true})
	case "server-strict":
		connectionLogger.Info("enabling SSL/TLS server strict mode")
		v.conn = tls.Client(v.conn, &tls.Config{ServerName: v.connURL.Hostname()})
	default:
		err := fmt.Errorf("unsupported tlsmode flag: %s - should be 'server', 'server-strict' or 'none'", sslFlag)
		connectionLogger.Error(err.Error())
		return err
	}
	// 	case "mutual":
	// 		err = fmt.Errorf("mutual ssl mode not currently supported")
	// 	default:
	// 		err = fmt.Errorf("unsupported ssl value in connect string: %s", sslFlag)

	return nil
}

func (v *connection) authSendPlainTextPassword() error {
	passwd, isSet := v.connURL.User.Password()

	if !isSet {
		passwd = ""
	}

	msg := &msgs.FEPasswordMsg{PasswordData: passwd}

	return v.sendMessage(msg)
}

func (v *connection) authSendMD5Password(extraAuthData []byte) error {
	passwd, isSet := v.connURL.User.Password()

	if !isSet {
		passwd = ""
	}

	hash1 := fmt.Sprintf("%x", md5.Sum([]byte(passwd+v.connURL.User.Username())))
	hash2 := fmt.Sprintf("md5%x", md5.Sum(append([]byte(hash1), extraAuthData[0:4]...)))

	msg := &msgs.FEPasswordMsg{PasswordData: hash2}

	return v.sendMessage(msg)
}

func (v *connection) authSendSHA512Password(extraAuthData []byte) error {
	passwd, isSet := v.connURL.User.Password()

	if !isSet {
		passwd = ""
	}

	hash1 := fmt.Sprintf("%x", sha512.Sum512(append([]byte(passwd), extraAuthData[8:]...)))
	hash2 := fmt.Sprintf("sha512%x", sha512.Sum512(append([]byte(hash1), extraAuthData[0:4]...)))

	msg := &msgs.FEPasswordMsg{PasswordData: hash2}

	return v.sendMessage(msg)
}
