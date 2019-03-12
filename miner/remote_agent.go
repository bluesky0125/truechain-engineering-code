// Copyright 2015 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

package miner

import (
	"sync"
	"sync/atomic"
	"time"

	"encoding/hex"
	"errors"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/log"
	"github.com/truechain/truechain-engineering-code/consensus"
	"github.com/truechain/truechain-engineering-code/core/types"
)

type hashrate struct {
	ping time.Time
	rate uint64
}

const UPDATABLOCKLENGTH = 12000 //12000  3000
const DATASETHEADLENGH = 10240

// RemoteAgent for Remote mine
type RemoteAgent struct {
	mu sync.Mutex

	quitCh   chan struct{}
	workCh   chan *Work
	returnCh chan<- *Result

	chain       consensus.ChainReader
	snailchain  consensus.SnailChainReader
	engine      consensus.Engine
	currentWork *Work
	work        map[common.Hash]*Work

	hashrateMu sync.RWMutex
	hashrate   map[common.Hash]hashrate

	running int32 // running indicates whether the agent is active. Call atomically
}

//NewRemoteAgent create remote agent object
func NewRemoteAgent(chain consensus.ChainReader, snailchain consensus.SnailChainReader, engine consensus.Engine) *RemoteAgent {

	return &RemoteAgent{
		chain:      chain,
		snailchain: snailchain,
		engine:     engine,
		work:       make(map[common.Hash]*Work),
		hashrate:   make(map[common.Hash]hashrate),
	}
}

//SubmitHashrate return the HashRate for remote agent
func (a *RemoteAgent) SubmitHashrate(id common.Hash, rate uint64) {
	a.hashrateMu.Lock()
	defer a.hashrateMu.Unlock()

	a.hashrate[id] = hashrate{time.Now(), rate}
}

// Work return a work chan
func (a *RemoteAgent) Work() chan<- *Work {
	return a.workCh
}

// SetReturnCh return a mine result for return chan
func (a *RemoteAgent) SetReturnCh(returnCh chan<- *Result) {
	a.returnCh = returnCh
}

//Start remote control the start mine
func (a *RemoteAgent) Start() {
	if !atomic.CompareAndSwapInt32(&a.running, 0, 1) {
		return
	}
	a.quitCh = make(chan struct{})
	a.workCh = make(chan *Work, 1)
	go a.loop(a.workCh, a.quitCh)
}

//Stop remote control the stop mine
func (a *RemoteAgent) Stop() {
	if !atomic.CompareAndSwapInt32(&a.running, 1, 0) {
		return
	}
	close(a.quitCh)
	close(a.workCh)
}

// GetHashRate returns the accumulated hashrate of all identifier combined
func (a *RemoteAgent) GetHashRate() (tot int64) {
	a.hashrateMu.RLock()
	defer a.hashrateMu.RUnlock()

	// this could overflow
	for _, hashrate := range a.hashrate {
		tot += int64(hashrate.rate)
	}
	return
}

//GetWork return the current block hash without nonce
func (a *RemoteAgent) GetWork() ([3]string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	var res [3]string

	if a.currentWork != nil {
		block := a.currentWork.Block
		block.Number()
		res[0] = block.HashNoNonce().Hex()
		DatasetHash := a.engine.DataSetHash(block.NumberU64())
		res[1] = hex.EncodeToString(DatasetHash)
		// Calculate the "target" to be returned to the external miner
		/*n := big.NewInt(1)
		n.Lsh(n, 255)
		n.Div(n, block.BlockDifficulty())
		n.Lsh(n, 1)
		res[2] = common.BytesToHash(n.Bytes()).Hex()*/
		//log.Info("------diff", "is", block.BlockDifficulty())
		res[2] = common.BytesToHash(block.FruitDifficulty().Bytes()).Hex()
		//log.Info("------res[2]", "is", res[2])
		a.work[block.HashNoNonce()] = a.currentWork
		return res, nil
	}
	return res, errors.New("No work available yet, Don't panic.")
}

// SubmitWork tries to inject a pow solution into the remote agent, returning
// whether the solution was accepted or not (not can be both a bad pow as well as
// any other error, like no work pending).
func (a *RemoteAgent) SubmitWork(nonce types.BlockNonce, mixDigest, hash common.Hash) bool {
	a.mu.Lock()
	defer a.mu.Unlock()

	log.Info("--------get submit work", "nonce", nonce, "mixDigest", mixDigest, "hash", hash)

	// Make sure the work submitted is present
	work := a.work[hash]
	if work == nil {
		log.Info("Work submitted but none pending", "hash", hash)
		return false
	}
	// Make sure the Engine solutions is indeed valid
	result := work.Block.Header()
	result.Nonce = nonce
	result.MixDigest = mixDigest

	//pointer := a.snailchain.GetHeaderByHash(result.PointerHash)
	if err := a.engine.VerifySnailSeal(a.snailchain, result, false); err != nil {
		log.Warn("Invalid proof-of-work submitted", "hash", hash, "err", err)
		return false
	}
	block := work.Block.WithSeal(result)

	//neo for result struct add fruit result with to change the fun
	//fruit :=work.Block.WithSeal(result)

	// Solutions seems to be valid, return to the miner and notify acceptance
	//a.returnCh <- &Result{work, block}

	//Neo 20180624
	a.returnCh <- &Result{work, block}

	delete(a.work, hash)

	return true
}

//GetWork return the current block hash without nonce
func (a *RemoteAgent) GetDataset() ([DATASETHEADLENGH][]byte, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	var res [DATASETHEADLENGH][]byte
	if a.currentWork != nil {
		block := a.currentWork.Block
		epoch := uint64((block.Number().Uint64() - 1) / UPDATABLOCKLENGTH)
		if epoch == 0 {
			return res, nil
		}
		st_block_num := uint64((epoch-1)*UPDATABLOCKLENGTH + 1)

		for i := 0; i < DATASETHEADLENGH; i++ {
			header := a.snailchain.GetHeaderByNumber(uint64(i) + st_block_num)
			if header == nil {
				//log.Error("----updateTBL--The skip is nil---- ", "blockNum is:  ", (uint64(i) + st_block_num))
				return res, errors.New("GetDataset get heard fial")
			}
			res[i] = header.Hash().Bytes()
		}
		return res, nil
	}
	return res, errors.New("No work available yet, Don't panic.")
}

// loop monitors mining events on the work and quit channels, updating the internal
// state of the remote miner until a termination is requested.
//
// Note, the reason the work and quit channels are passed as parameters is because
// RemoteAgent.Start() constantly recreates these channels, so the loop code cannot
// assume data stability in these member fields.
func (a *RemoteAgent) loop(workCh chan *Work, quitCh chan struct{}) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-quitCh:
			return
		case work := <-workCh:
			a.mu.Lock()
			a.currentWork = work
			a.mu.Unlock()
		case <-ticker.C:
			// cleanup
			a.mu.Lock()
			for hash, work := range a.work {
				if time.Since(work.createdAt) > 7*(12*time.Second) {
					delete(a.work, hash)
				}
			}
			a.mu.Unlock()

			a.hashrateMu.Lock()
			for id, hashrate := range a.hashrate {
				if time.Since(hashrate.ping) > 10*time.Second {
					delete(a.hashrate, id)
				}
			}
			a.hashrateMu.Unlock()
		}
	}
}
