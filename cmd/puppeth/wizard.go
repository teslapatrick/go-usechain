// Copyright 2017 The go-ethereum Authors
// This file is part of go-ethereum.
//
// go-ethereum is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// go-ethereum is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU General Public License for more details.
//
// You should have received a copy of the GNU General Public License
// along with go-ethereum. If not, see <http://www.gnu.org/licenses/>.

package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"github.com/usechain/go-usechain/common/hexutil"
	"io/ioutil"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/usechain/go-usechain/common"
	"github.com/usechain/go-usechain/core"
	"github.com/usechain/go-usechain/log"
	"golang.org/x/crypto/ssh/terminal"
)

// config contains all the configurations needed by puppeth that should be saved
// between sessions.
type config struct {
	path      string   // File containing the configuration values
	bootnodes []string // Bootnodes to always connect to by all nodes
	ethstats  string   // Ethstats settings to cache for node deploys

	Genesis *core.Genesis     `json:"genesis,omitempty"` // Genesis block to cache for node deploys
	Servers map[string][]byte `json:"servers,omitempty"`
}

// servers retrieves an alphabetically sorted list of servers.
func (c config) servers() []string {
	servers := make([]string, 0, len(c.Servers))
	for server := range c.Servers {
		servers = append(servers, server)
	}
	sort.Strings(servers)

	return servers
}

// flush dumps the contents of config to disk.
func (c config) flush() {
	os.MkdirAll(filepath.Dir(c.path), 0755)

	out, _ := json.MarshalIndent(c, "", "  ")
	if err := ioutil.WriteFile(c.path, out, 0644); err != nil {
		log.Warn("Failed to save puppeth configs", "file", c.path, "err", err)
	}
}

type wizard struct {
	network string // Network name to manage
	conf    config // Configurations from previous runs

	servers  map[string]*sshClient // SSH connections to servers to administer
	services map[string][]string   // Ethereum services known to be running on servers

	in   *bufio.Reader // Wrapper around stdin to allow reading user input
	lock sync.Mutex    // Lock to protect configs during concurrent service discovery
}

// read reads a single line from stdin, trimming if from spaces.
func (w *wizard) read() string {
	fmt.Printf("> ")
	text, err := w.in.ReadString('\n')
	if err != nil {
		log.Crit("Failed to read user input", "err", err)
	}
	return strings.TrimSpace(text)
}

// readString reads a single line from stdin, trimming if from spaces, enforcing
// non-emptyness.
func (w *wizard) readString() string {
	for {
		fmt.Printf("> ")
		text, err := w.in.ReadString('\n')
		if err != nil {
			log.Crit("Failed to read user input", "err", err)
		}
		if text = strings.TrimSpace(text); text != "" {
			return text
		}
	}
}

// readDefaultString reads a single line from stdin, trimming if from spaces. If
// an empty line is entered, the default value is returned.
func (w *wizard) readDefaultString(def string) string {
	fmt.Printf("> ")
	text, err := w.in.ReadString('\n')
	if err != nil {
		log.Crit("Failed to read user input", "err", err)
	}
	if text = strings.TrimSpace(text); text != "" {
		return text
	}
	return def
}

// readInt reads a single line from stdin, trimming if from spaces, enforcing it
// to parse into an integer.
func (w *wizard) readInt() int {
	for {
		fmt.Printf("> ")
		text, err := w.in.ReadString('\n')
		if err != nil {
			log.Crit("Failed to read user input", "err", err)
		}
		if text = strings.TrimSpace(text); text == "" {
			continue
		}
		val, err := strconv.Atoi(strings.TrimSpace(text))
		if err != nil {
			log.Error("Invalid input, expected integer", "err", err)
			continue
		}
		return val
	}
}

// readDefaultInt reads a single line from stdin, trimming if from spaces, enforcing
// it to parse into an integer. If an empty line is entered, the default value is
// returned.
func (w *wizard) readDefaultInt(def int) int {
	for {
		fmt.Printf("> ")
		text, err := w.in.ReadString('\n')
		if err != nil {
			log.Crit("Failed to read user input", "err", err)
		}
		if text = strings.TrimSpace(text); text == "" {
			return def
		}
		val, err := strconv.Atoi(strings.TrimSpace(text))
		if err != nil {
			log.Error("Invalid input, expected integer", "err", err)
			continue
		}
		return val
	}
}

// readDefaultBigInt reads a single line from stdin, trimming if from spaces,
// enforcing it to parse into a big integer. If an empty line is entered, the
// default value is returned.
func (w *wizard) readDefaultBigInt(def *big.Int) *big.Int {
	for {
		fmt.Printf("> ")
		text, err := w.in.ReadString('\n')
		if err != nil {
			log.Crit("Failed to read user input", "err", err)
		}
		if text = strings.TrimSpace(text); text == "" {
			return def
		}
		val, ok := new(big.Int).SetString(text, 0)
		if !ok {
			log.Error("Invalid input, expected big integer")
			continue
		}
		return val
	}
}

/*
// readFloat reads a single line from stdin, trimming if from spaces, enforcing it
// to parse into a float.
func (w *wizard) readFloat() float64 {
	for {
		fmt.Printf("> ")
		text, err := w.in.ReadString('\n')
		if err != nil {
			log.Crit("Failed to read user input", "err", err)
		}
		if text = strings.TrimSpace(text); text == "" {
			continue
		}
		val, err := strconv.ParseFloat(strings.TrimSpace(text), 64)
		if err != nil {
			log.Error("Invalid input, expected float", "err", err)
			continue
		}
		return val
	}
}
*/

// readDefaultFloat reads a single line from stdin, trimming if from spaces, enforcing
// it to parse into a float. If an empty line is entered, the default value is returned.
func (w *wizard) readDefaultFloat(def float64) float64 {
	for {
		fmt.Printf("> ")
		text, err := w.in.ReadString('\n')
		if err != nil {
			log.Crit("Failed to read user input", "err", err)
		}
		if text = strings.TrimSpace(text); text == "" {
			return def
		}
		val, err := strconv.ParseFloat(strings.TrimSpace(text), 64)
		if err != nil {
			log.Error("Invalid input, expected float", "err", err)
			continue
		}
		return val
	}
}

// readPassword reads a single line from stdin, trimming it from the trailing new
// line and returns it. The input will not be echoed.
func (w *wizard) readPassword() string {
	fmt.Printf("> ")
	text, err := terminal.ReadPassword(int(os.Stdin.Fd()))
	if err != nil {
		log.Crit("Failed to read password", "err", err)
	}
	fmt.Println()
	return string(text)
}

// readAddress reads a single line from stdin, trimming if from spaces and converts
// it to an Ethereum address.
func (w *wizard) readAddress() *common.Address {
	for {
		// Read the address from the user
		fmt.Printf("> 0x")
		text, err := w.in.ReadString('\n')
		if err != nil {
			log.Crit("Failed to read user input", "err", err)
		}
		if text = strings.TrimSpace(text); text == "" {
			return nil
		}
		// Make sure it looks ok and return it if so
		if len(text) != 40 {
			log.Error("Invalid address length, please retry")
			continue
		}
		bigaddr, _ := new(big.Int).SetString(text, 16)
		address := common.BigToAddress(bigaddr)
		return &address
	}
}

func (w *wizard) readMinerCodeAndStorj() ([]byte, map[common.Hash]common.Hash) {
	// prepare storage
	var storj map[common.Hash]common.Hash
	storj = make(map[common.Hash]common.Hash)
	fmt.Println()
	fmt.Println("Add your contract bytecode !")
	fmt.Printf("> 0x")
	// storj bytecode
	var bytecode string
	var b        []byte
	for {
		bytecode = w.readString()
		b, _ = hexutil.Decode(bytecode)
		if len(bytecode) == 0 {
			fmt.Println("Pls Add your contract bytecode!")
			continue
		}
		break
	}
	// storj key & value
	storj = w.readContractStorage()
	return b, storj
}

func (w *wizard) readContractStorage() map[common.Hash]common.Hash {
	var storj map[common.Hash]common.Hash
	storj = make(map[common.Hash]common.Hash)
	var tempRpowMinerHash common.Hash
	for {
		fmt.Println()
		fmt.Println("Add new contract storage key (advisable at least one)")
		fmt.Printf("> 0x")
		key, err := w.in.ReadString('\n')
		if err != nil {
			log.Error("Failed to read user input", "err", err)
			continue
		}
		if key = strings.TrimSpace(key); key == "" {
			//log.Error("Pls add contract storage key", "err", "Need to add new storage key")
			return storj
		}
		tempRpowMinerHash = common.HexToHash(key)

		fmt.Println()
		fmt.Println("Add value to the contract storage key(advisable at least one)")
		fmt.Printf("> 0x")
		if value, _ := w.in.ReadString('\n'); value != "" {
			storj[tempRpowMinerHash] = common.HexToHash(value)
		} else {
			log.Error("Restart: Pls add value to the contract storage", "err", "Need to add new value")
			continue
		}

	}

}

// readDefaultAddress reads a single line from stdin, trimming if from spaces and
// converts it to an Ethereum address. If an empty line is entered, the default
// value is returned.
func (w *wizard) readDefaultAddress(def common.Address) common.Address {
	for {
		// Read the address from the user
		fmt.Printf("> 0x")
		text, err := w.in.ReadString('\n')
		if err != nil {
			log.Crit("Failed to read user input", "err", err)
		}
		if text = strings.TrimSpace(text); text == "" {
			return def
		}
		// Make sure it looks ok and return it if so
		if len(text) != 40 {
			log.Error("Invalid address length, please retry")
			continue
		}
		bigaddr, _ := new(big.Int).SetString(text, 16)
		return common.BigToAddress(bigaddr)
	}
}

// readJSON reads a raw JSON message and returns it.
func (w *wizard) readJSON() string {
	var blob json.RawMessage

	for {
		fmt.Printf("> ")
		if err := json.NewDecoder(w.in).Decode(&blob); err != nil {
			log.Error("Invalid JSON, please try again", "err", err)
			continue
		}
		return string(blob)
	}
}

// readIPAddress reads a single line from stdin, trimming if from spaces and
// returning it if it's convertible to an IP address. The reason for keeping
// the user input format instead of returning a Go net.IP is to match with
// weird formats used by ethstats, which compares IPs textually, not by value.
func (w *wizard) readIPAddress() string {
	for {
		// Read the IP address from the user
		fmt.Printf("> ")
		text, err := w.in.ReadString('\n')
		if err != nil {
			log.Crit("Failed to read user input", "err", err)
		}
		if text = strings.TrimSpace(text); text == "" {
			return ""
		}
		// Make sure it looks ok and return it if so
		if ip := net.ParseIP(text); ip == nil {
			log.Error("Invalid IP address, please retry")
			continue
		}
		return text
	}
}
// readDefaultYesNo reads a single line from stdin, trimming if from spaces and
// interpreting it as a 'yes' or a 'no'. If an empty line is entered, the default
// value is returned.
func (w *wizard) readDefaultYesNo(def bool) bool {
	for {
		fmt.Printf("> ")
		text, err := w.in.ReadString('\n')
		if err != nil {
			log.Crit("Failed to read user input", "err", err)
		}
		if text = strings.ToLower(strings.TrimSpace(text)); text == "" {
			return def
		}
		if text == "y" || text == "yes" {
			return true
		}
		if text == "n" || text == "no" {
			return false
		}
		log.Error("Invalid input, expected 'y', 'yes', 'n', 'no' or empty")
	}
}

// return the string data that has been added to the num
func IncreaseHexByNum(indexKeyHash []byte, num int64) []byte {
	x := big.NewInt(0)
	y := big.NewInt(int64(num))
	x.SetBytes(indexKeyHash)
	x.Add(x, y)
	return x.Bytes()
}

