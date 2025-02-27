package msgs

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

import "fmt"

// FEStartupMsg docs
type FEStartupMsg struct {
	ProtocolVersion uint32
	DriverName      string
	DriverVersion   string
	Username        string
	Database        string
	SessionID       string
	ClientPID       int
}

// Flatten docs
func (m *FEStartupMsg) Flatten() ([]byte, byte) {

	buf := newMsgBuffer()

	buf.appendUint32(m.ProtocolVersion)

	if len(m.Username) > 0 {
		buf.appendLabeledString("user", m.Username)
	}

	if len(m.Database) > 0 {
		buf.appendLabeledString("database", m.Database)
	}

	buf.appendLabeledString("client_type", m.DriverName)
	buf.appendLabeledString("client_version", m.DriverVersion)
	buf.appendLabeledString("client_label", m.SessionID)
	buf.appendLabeledString("client_pid", fmt.Sprintf("%d", m.ClientPID))
	buf.appendBytes([]byte{0})

	return buf.bytes(), 0
}

func (m *FEStartupMsg) String() string {
	return fmt.Sprintf(
		"Startup (packet): ProtocolVersion:%08X, DriverName='%s', DriverVersion='%s', UserName='%s', Database='%s', SessionID='%s', ClientPID=%d",
		m.ProtocolVersion,
		m.DriverName,
		m.DriverVersion,
		m.Username,
		m.Database,
		m.SessionID,
		m.ClientPID)
}
