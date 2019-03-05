// Copyright 2014 The go-ethereum Authors
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

package core

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/common/math"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/truechain/truechain-engineering-code/core/rawdb"
	snaildb "github.com/truechain/truechain-engineering-code/core/snailchain/rawdb"
	"github.com/truechain/truechain-engineering-code/core/state"
	"github.com/truechain/truechain-engineering-code/core/types"
	"github.com/truechain/truechain-engineering-code/etruedb"
	"github.com/truechain/truechain-engineering-code/params"
)

//go:generate gencodec -type Genesis -field-override genesisSpecMarshaling -out gen_genesis.go
//go:generate gencodec -type GenesisAccount -field-override genesisAccountMarshaling -out gen_genesis_account.go

var errGenesisNoConfig = errors.New("genesis has no chain configuration")

// Genesis specifies the header fields, state of a genesis block. It also defines hard
// fork switch-over blocks through the chain configuration.
type Genesis struct {
	Config     *params.ChainConfig      `json:"config"`
	Nonce      uint64                   `json:"nonce"`
	Timestamp  uint64                   `json:"timestamp"`
	ExtraData  []byte                   `json:"extraData"`
	GasLimit   uint64                   `json:"gasLimit"   gencodec:"required"`
	Difficulty *big.Int                 `json:"difficulty" gencodec:"required"`
	Mixhash    common.Hash              `json:"mixHash"`
	Coinbase   common.Address           `json:"coinbase"`
	Alloc      types.GenesisAlloc       `json:"alloc"      gencodec:"required"`
	Committee  []*types.CommitteeMember `json:"committee"      gencodec:"required"`

	// These fields are used for consensus tests. Please don't use them
	// in actual genesis blocks.
	Number     uint64      `json:"number"`
	GasUsed    uint64      `json:"gasUsed"`
	ParentHash common.Hash `json:"parentHash"`
}

// GenesisAccount is an account in the state of the genesis block.
type GenesisAccount struct {
	Code       []byte                      `json:"code,omitempty"`
	Storage    map[common.Hash]common.Hash `json:"storage,omitempty"`
	Balance    *big.Int                    `json:"balance" gencodec:"required"`
	Nonce      uint64                      `json:"nonce,omitempty"`
	PrivateKey []byte                      `json:"secretKey,omitempty"` // for tests
}

// field type overrides for gencodec
type genesisSpecMarshaling struct {
	Nonce      math.HexOrDecimal64
	Timestamp  math.HexOrDecimal64
	ExtraData  hexutil.Bytes
	GasLimit   math.HexOrDecimal64
	GasUsed    math.HexOrDecimal64
	Number     math.HexOrDecimal64
	Difficulty *math.HexOrDecimal256
	Alloc      map[common.UnprefixedAddress]GenesisAccount
}

type genesisAccountMarshaling struct {
	Code       hexutil.Bytes
	Balance    *math.HexOrDecimal256
	Nonce      math.HexOrDecimal64
	Storage    map[storageJSON]storageJSON
	PrivateKey hexutil.Bytes
}

// storageJSON represents a 256 bit byte array, but allows less than 256 bits when
// unmarshaling from hex.
type storageJSON common.Hash

func (h *storageJSON) UnmarshalText(text []byte) error {
	text = bytes.TrimPrefix(text, []byte("0x"))
	if len(text) > 64 {
		return fmt.Errorf("too many hex characters in storage key/value %q", text)
	}
	offset := len(h) - len(text)/2 // pad on the left
	if _, err := hex.Decode(h[offset:], text); err != nil {
		fmt.Println(err)
		return fmt.Errorf("invalid hex storage key/value %q", text)
	}
	return nil
}

func (h storageJSON) MarshalText() ([]byte, error) {
	return hexutil.Bytes(h[:]).MarshalText()
}

// GenesisMismatchError is raised when trying to overwrite an existing
// genesis block with an incompatible one.
type GenesisMismatchError struct {
	Stored, New common.Hash
}

func (e *GenesisMismatchError) Error() string {
	return fmt.Sprintf("database already contains an incompatible genesis block (have %x, new %x)", e.Stored[:8], e.New[:8])
}

// SetupGenesisBlock writes or updates the genesis block in db.
// The block that will be used is:
//
//                          genesis == nil       genesis != nil
//                       +------------------------------------------
//     db has no genesis |  main-net default  |  genesis
//     db has genesis    |  from DB           |  genesis (if compatible)
//
// The stored chain configuration will be updated if it is compatible (i.e. does not
// specify a fork block below the local head block). In case of a conflict, the
// error is a *params.ConfigCompatError and the new, unwritten config is returned.
//
// The returned chain configuration is never nil.
func SetupGenesisBlock(db etruedb.Database, genesis *Genesis) (*params.ChainConfig, common.Hash, common.Hash, error) {
	if genesis != nil && genesis.Config == nil {
		return params.AllMinervaProtocolChanges, common.Hash{}, common.Hash{}, errGenesisNoConfig
	}

	fastConfig, fastHash, fastErr := setupFastGenesisBlock(db, genesis)
	_, snailHash, _ := setupSnailGenesisBlock(db, genesis)

	return fastConfig, fastHash, snailHash, fastErr

}

// setupFastGenesisBlock writes or updates the fast genesis block in db.
// The block that will be used is:
//
//                          genesis == nil       genesis != nil
//                       +------------------------------------------
//     db has no genesis |  main-net default  |  genesis
//     db has genesis    |  from DB           |  genesis (if compatible)
//
// The stored chain configuration will be updated if it is compatible (i.e. does not
// specify a fork block below the local head block). In case of a conflict, the
// error is a *params.ConfigCompatError and the new, unwritten config is returned.
//
// The returned chain configuration is never nil.
func setupFastGenesisBlock(db etruedb.Database, genesis *Genesis) (*params.ChainConfig, common.Hash, error) {
	if genesis != nil && genesis.Config == nil {
		return params.AllMinervaProtocolChanges, common.Hash{}, errGenesisNoConfig
	}

	// Just commit the new block if there is no stored genesis block.
	stored := rawdb.ReadCanonicalHash(db, 0)
	if (stored == common.Hash{}) {
		if genesis == nil {
			log.Info("Writing default main-net genesis block")
			genesis = DefaultGenesisBlock()
		} else {
			log.Info("Writing custom genesis block")
		}
		block, err := genesis.CommitFast(db)
		return genesis.Config, block.Hash(), err
	}

	// Check whether the genesis block is already written.
	if genesis != nil {
		hash := genesis.ToFastBlock(nil).Hash()
		if hash != stored {
			return genesis.Config, hash, &GenesisMismatchError{stored, hash}
		}
	}

	// Get the existing chain configuration.
	newcfg := genesis.configOrDefault(stored)
	storedcfg := rawdb.ReadChainConfig(db, stored)
	if storedcfg == nil {
		log.Warn("Found genesis block without chain config")
		rawdb.WriteChainConfig(db, stored, newcfg)
		return newcfg, stored, nil
	}
	// Special case: don't change the existing config of a non-mainnet chain if no new
	// config is supplied. These chains would get AllProtocolChanges (and a compat error)
	// if we just continued here.
	if genesis == nil && stored != params.MainnetGenesisHash {
		return storedcfg, stored, nil
	}

	// Check config compatibility and write the config. Compatibility errors
	// are returned to the caller unless we're already at block zero.
	height := rawdb.ReadHeaderNumber(db, rawdb.ReadHeadHeaderHash(db))
	if height == nil {
		return newcfg, stored, fmt.Errorf("missing block number for head header hash")
	}
	compatErr := storedcfg.CheckCompatible(newcfg, *height)
	if compatErr != nil && *height != 0 && compatErr.RewindTo != 0 {
		return newcfg, stored, compatErr
	}
	rawdb.WriteChainConfig(db, stored, newcfg)
	return newcfg, stored, nil
}

// CommitFast writes the block and state of a genesis specification to the database.
// The block is committed as the canonical head block.
func (g *Genesis) CommitFast(db etruedb.Database) (*types.Block, error) {
	block := g.ToFastBlock(db)
	if block.Number().Sign() != 0 {
		return nil, fmt.Errorf("can't commit genesis block with number > 0")
	}
	//rawdb.WriteTd(db, block.Hash(), block.NumberU64(), g.Difficulty)
	rawdb.WriteBlock(db, block)
	rawdb.WriteReceipts(db, block.Hash(), block.NumberU64(), nil)
	rawdb.WriteCanonicalHash(db, block.Hash(), block.NumberU64())
	rawdb.WriteHeadBlockHash(db, block.Hash())
	rawdb.WriteHeadHeaderHash(db, block.Hash())
	rawdb.WriteStateGcBR(db, block.NumberU64())

	config := g.Config
	if config == nil {
		config = params.AllMinervaProtocolChanges
	}
	rawdb.WriteChainConfig(db, block.Hash(), config)
	return block, nil
}

// ToFastBlock creates the genesis block and writes state of a genesis specification
// to the given database (or discards it if nil).
func (g *Genesis) ToFastBlock(db etruedb.Database) *types.Block {
	if db == nil {
		db = etruedb.NewMemDatabase()
	}
	statedb, _ := state.New(common.Hash{}, state.NewDatabase(db))
	for addr, account := range g.Alloc {
		statedb.AddBalance(addr, account.Balance)
		statedb.SetCode(addr, account.Code)
		statedb.SetNonce(addr, account.Nonce)
		for key, value := range account.Storage {
			statedb.SetState(addr, key, value)
		}
	}
	root := statedb.IntermediateRoot(false)
	head := &types.Header{
		Number:     new(big.Int).SetUint64(g.Number),
		Time:       new(big.Int).SetUint64(g.Timestamp),
		ParentHash: g.ParentHash,
		Extra:      g.ExtraData,
		GasLimit:   g.GasLimit,
		GasUsed:    g.GasUsed,
		Root:       root,
	}
	if g.GasLimit == 0 {
		head.GasLimit = params.GenesisGasLimit
	}

	statedb.Commit(false)
	statedb.Database().TrieDB().Commit(root, true)

	// All genesis committee members are included in switchinfo of block #0
	committee := &types.SwitchInfos{CID: common.Big0, Members: g.Committee, BackMembers: make([]*types.CommitteeMember, 0), Vals: make([]*types.SwitchEnter, 0)}
	for _, member := range committee.Members {
		pubkey, _ := crypto.UnmarshalPubkey(member.Publickey)
		member.Flag = types.StateUsedFlag
		member.MType = types.TypeFixed
		member.CommitteeBase = crypto.PubkeyToAddress(*pubkey)
	}
	return types.NewBlock(head, nil, nil, nil, committee.Members)
}

// MustFastCommit writes the genesis block and state to db, panicking on error.
// The block is committed as the canonical head block.
func (g *Genesis) MustFastCommit(db etruedb.Database) *types.Block {
	block, err := g.CommitFast(db)
	if err != nil {
		panic(err)
	}
	return block
}

// setupSnailGenesisBlock writes or updates the genesis snail block in db.
// The block that will be used is:
//
//                          genesis == nil       genesis != nil
//                       +------------------------------------------
//     db has no genesis |  main-net default  |  genesis
//     db has genesis    |  from DB           |  genesis (if compatible)
//
// The stored chain configuration will be updated if it is compatible (i.e. does not
// specify a fork block below the local head block). In case of a conflict, the
// error is a *params.ConfigCompatError and the new, unwritten config is returned.
//
// The returned chain configuration is never nil.
func setupSnailGenesisBlock(db etruedb.Database, genesis *Genesis) (*params.ChainConfig, common.Hash, error) {
	if genesis != nil && genesis.Config == nil {
		return params.AllMinervaProtocolChanges, common.Hash{}, errGenesisNoConfig
	}
	// Just commit the new block if there is no stored genesis block.
	stored := snaildb.ReadCanonicalHash(db, 0)
	if (stored == common.Hash{}) {
		if genesis == nil {
			log.Info("Writing default main-net genesis block")
			genesis = DefaultGenesisBlock()
		} else {
			log.Info("Writing custom genesis block")
		}
		block, err := genesis.CommitSnail(db)
		return genesis.Config, block.Hash(), err
	}

	// Check whether the genesis block is already written.
	if genesis != nil {
		hash := genesis.ToSnailBlock(nil).Hash()
		if hash != stored {
			return genesis.Config, hash, &GenesisMismatchError{stored, hash}
		}
	}

	// Get the existing chain configuration.
	newcfg := genesis.configOrDefault(stored)
	return newcfg, stored, nil
}

// ToSnailBlock creates the genesis block and writes state of a genesis specification
// to the given database (or discards it if nil).
func (g *Genesis) ToSnailBlock(db etruedb.Database) *types.SnailBlock {
	if db == nil {
		db = etruedb.NewMemDatabase()
	}

	head := &types.SnailHeader{
		Number:     new(big.Int).SetUint64(g.Number),
		Nonce:      types.EncodeNonce(g.Nonce),
		Time:       new(big.Int).SetUint64(g.Timestamp),
		ParentHash: g.ParentHash,
		Extra:      g.ExtraData,
		Difficulty: g.Difficulty,
		MixDigest:  g.Mixhash,
		Coinbase:   g.Coinbase,
	}

	if g.Difficulty == nil {
		head.Difficulty = params.GenesisDifficulty
	}

	fastBlock := g.ToFastBlock(db)
	fruitHead := &types.SnailHeader{
		Number:          new(big.Int).SetUint64(g.Number),
		Nonce:           types.EncodeNonce(g.Nonce),
		Time:            new(big.Int).SetUint64(g.Timestamp),
		ParentHash:      g.ParentHash,
		FastNumber:      fastBlock.Number(),
		FastHash:        fastBlock.Hash(),
		FruitDifficulty: g.Difficulty,
		Coinbase:        g.Coinbase,
	}
	fruit := types.NewSnailBlock(fruitHead, nil, nil, nil)

	return types.NewSnailBlock(head, []*types.SnailBlock{fruit}, nil, nil)
}

// CommitSnail writes the block and state of a genesis specification to the database.
// The block is committed as the canonical head block.
func (g *Genesis) CommitSnail(db etruedb.Database) (*types.SnailBlock, error) {
	block := g.ToSnailBlock(db)
	if block.Number().Sign() != 0 {
		return nil, fmt.Errorf("can't commit genesis block with number > 0")
	}
	snaildb.WriteTd(db, block.Hash(), block.NumberU64(), g.Difficulty)
	snaildb.WriteBlock(db, block)
	//snaildb.WriteReceipts(db, block.Hash(), block.NumberU64(), nil)
	snaildb.WriteCanonicalHash(db, block.Hash(), block.NumberU64())
	snaildb.WriteHeadBlockHash(db, block.Hash())
	snaildb.WriteHeadHeaderHash(db, block.Hash())

	// config := g.Config
	// if config == nil {
	// 	config = params.AllMinervaProtocolChanges
	// }
	// snaildb.WriteChainConfig(db, block.Hash(), config)
	return block, nil
}

// MustSnailCommit writes the genesis block and state to db, panicking on error.
// The block is committed as the canonical head block.
func (g *Genesis) MustSnailCommit(db etruedb.Database) *types.SnailBlock {
	block, err := g.CommitSnail(db)
	if err != nil {
		panic(err)
	}
	return block
}

// DefaultGenesisBlock returns the Truechain main net snail block.
func DefaultGenesisBlock() *Genesis {
	i, _ := new(big.Int).SetString("90000000000000000000000", 10)
	key1 := hexutil.MustDecode("0x0488a25849abee5921fdb581ba34cd66adc8e02b108391c4153ca8da27722e16badf4fcd5ba7f557ae76d444ccf3638e4590a181805623de1cab67f31364c79736")
	key2 := hexutil.MustDecode("0x04a9a1cedb8900d893b607c4dbc834abada3fe98f247b8bcb5ef44d3d3a246c4cf41d9d792527473c30ded81fa4b81afe7030a09e093dd92746b98c79e6a204c63")
	key3 := hexutil.MustDecode("0x040d153624462927444a8212717e4ad41ec5f5739bc36598d093d114729e1dc782d55d322699705829cf9d69f201009db797ebe8ba952f10a26fe36c64356b111b")
	key4 := hexutil.MustDecode("0x04a3474c26578fce00d241119758271f6a208cc987c6f37d1518dcea2a51257bafeebd93202ae499cb5a8986720d4b63a04043aadb4d03430194a81860c9ca0763")

	return &Genesis{
		Config:     params.MainnetChainConfig,
		Nonce:      928,
		ExtraData:  nil,
		GasLimit:   88080384,
		Difficulty: big.NewInt(20000),
		//Alloc:      decodePrealloc(mainnetAllocData),
		Alloc: map[common.Address]types.GenesisAccount{
			common.HexToAddress("0x7c357530174275dd30e46319b89f71186256e4f7"): {Balance: i},
			common.HexToAddress("0x4cf807958b9f6d9fd9331397d7a89a079ef43288"): {Balance: i},
			common.HexToAddress("0x04d2252a3e0ca7c2aa81247ca33060855a34a808"): {Balance: i},
			common.HexToAddress("0x05712ff78d08eaf3e0f1797aaf4421d9b24f8679"): {Balance: i},
			common.HexToAddress("0x764727f61dd0717a48236842435e9aefab6723c3"): {Balance: i},
			common.HexToAddress("0x764986534dba541d5061e04b9c561abe3f671178"): {Balance: i},
			common.HexToAddress("0x0fd0bbff2e5b3ddb4f030ff35eb0fe06658646cf"): {Balance: i},
			common.HexToAddress("0x40b3a743ba285a20eaeee770d37c093276166568"): {Balance: i},
			common.HexToAddress("0x9d3c4a33d3bcbd2245a1bebd8e989b696e561eae"): {Balance: i},
			common.HexToAddress("0x35c9d83c3de709bbd2cb4a8a42b89e0317abe6d4"): {Balance: i},
		},
		Committee: []*types.CommitteeMember{
			&types.CommitteeMember{Coinbase: common.HexToAddress("0x76ea2f3a002431fede1141b660dbb75c26ba6d97"), Publickey: key1},
			&types.CommitteeMember{Coinbase: common.HexToAddress("0x831151b7eb8e650dc442cd623fbc6ae20279df85"), Publickey: key2},
			&types.CommitteeMember{Coinbase: common.HexToAddress("0x1074f7deccf8c66efcd0106e034d3356b7db3f2c"), Publickey: key3},
			&types.CommitteeMember{Coinbase: common.HexToAddress("0xd985e9871d1be109af5a7f6407b1d6b686901fff"), Publickey: key4},
		},
	}
}

func (g *Genesis) configOrDefault(ghash common.Hash) *params.ChainConfig {
	switch {
	case g != nil:
		return g.Config
	case ghash == params.MainnetGenesisHash:
		return params.MainnetChainConfig
	case ghash == params.MainnetSnailGenesisHash:
		return params.MainnetChainConfig
	case ghash == params.TestnetGenesisSHash:
		return params.TestnetChainConfig
	case ghash == params.TestnetSnailGenesisHash:
		return params.TestnetChainConfig
	default:
		return params.AllMinervaProtocolChanges
	}
}

func decodePrealloc(data string) types.GenesisAlloc {
	var p []struct{ Addr, Balance *big.Int }
	if err := rlp.NewStream(strings.NewReader(data), 0).Decode(&p); err != nil {
		panic(err)
	}
	ga := make(types.GenesisAlloc, len(p))
	for _, account := range p {
		ga[common.BigToAddress(account.Addr)] = types.GenesisAccount{Balance: account.Balance}
	}
	return ga
}

// GenesisFastBlockForTesting creates and writes a block in which addr has the given wei balance.
func GenesisFastBlockForTesting(db etruedb.Database, addr common.Address, balance *big.Int) *types.Block {
	g := Genesis{Alloc: types.GenesisAlloc{addr: {Balance: balance}}}
	return g.MustFastCommit(db)
}

// GenesisSnailBlockForTesting creates and writes a block in which addr has the given wei balance.
func GenesisSnailBlockForTesting(db etruedb.Database, addr common.Address, balance *big.Int) *types.SnailBlock {
	g := Genesis{Alloc: types.GenesisAlloc{addr: {Balance: balance}}}
	return g.MustSnailCommit(db)
}

// DefaultDevGenesisBlock returns the Rinkeby network genesis block.
func DefaultDevGenesisBlock() *Genesis {
	i, _ := new(big.Int).SetString("90000000000000000000000", 10)
	key1 := hexutil.MustDecode("0x0488a25849abee5921fdb581ba34cd66adc8e02b108391c4153ca8da27722e16badf4fcd5ba7f557ae76d444ccf3638e4590a181805623de1cab67f31364c79736")
	key2 := hexutil.MustDecode("0x04a9a1cedb8900d893b607c4dbc834abada3fe98f247b8bcb5ef44d3d3a246c4cf41d9d792527473c30ded81fa4b81afe7030a09e093dd92746b98c79e6a204c63")
	key3 := hexutil.MustDecode("0x040d153624462927444a8212717e4ad41ec5f5739bc36598d093d114729e1dc782d55d322699705829cf9d69f201009db797ebe8ba952f10a26fe36c64356b111b")
	key4 := hexutil.MustDecode("0x04a3474c26578fce00d241119758271f6a208cc987c6f37d1518dcea2a51257bafeebd93202ae499cb5a8986720d4b63a04043aadb4d03430194a81860c9ca0763")
	key5 := hexutil.MustDecode("0x04a3e174523b1054e14f123580bce258745e65591c2a4ee44764e55eb87a3782c9920d306e6121d4f10f8726800497ad9ca5a0bfdfe0832779dbaf7b95b3bf0111")
	key6 := hexutil.MustDecode("0x04d370defb1b7b8c086f98c4a7d7b90348b088cd2effdcc27b86feebdff499a192b4a5a5b16a400625271d69b3fa7d8c42c8b2e15c910cd1f314f28eb5beb73342")
	key7 := hexutil.MustDecode("0x04f67ab0cd48f626da89c718bcd909a04dea393d632d3191891539ef2f5ff6bb1e5d340ebe94cb6d9126b26e1ec64bb4783e9e8ddf31346b53d651d15eb226142e")

	return &Genesis{
		Config:     params.DevnetChainConfig,
		Nonce:      928,
		ExtraData:  nil,
		GasLimit:   88080384,
		Difficulty: big.NewInt(20000),
		//Alloc:      decodePrealloc(mainnetAllocData),
		Alloc: map[common.Address]types.GenesisAccount{
			common.HexToAddress("0x7c357530174275dd30e46319b89f71186256e4f7"): {Balance: i},
			common.HexToAddress("0x4cf807958b9f6d9fd9331397d7a89a079ef43288"): {Balance: i},
			common.HexToAddress("0x04d2252a3e0ca7c2aa81247ca33060855a34a808"): {Balance: i},
			common.HexToAddress("0x05712ff78d08eaf3e0f1797aaf4421d9b24f8679"): {Balance: i},
			common.HexToAddress("0x764727f61dd0717a48236842435e9aefab6723c3"): {Balance: i},
			common.HexToAddress("0x764986534dba541d5061e04b9c561abe3f671178"): {Balance: i},
			common.HexToAddress("0x0fd0bbff2e5b3ddb4f030ff35eb0fe06658646cf"): {Balance: i},
			common.HexToAddress("0x40b3a743ba285a20eaeee770d37c093276166568"): {Balance: i},
			common.HexToAddress("0x9d3c4a33d3bcbd2245a1bebd8e989b696e561eae"): {Balance: i},
			common.HexToAddress("0x35c9d83c3de709bbd2cb4a8a42b89e0317abe6d4"): {Balance: i},
		},
		Committee: []*types.CommitteeMember{
			{Coinbase: common.HexToAddress("0x76ea2f3a002431fede1141b660dbb75c26ba6d97"), Publickey: key1},
			{Coinbase: common.HexToAddress("0x831151b7eb8e650dc442cd623fbc6ae20279df85"), Publickey: key2},
			{Coinbase: common.HexToAddress("0x1074f7deccf8c66efcd0106e034d3356b7db3f2c"), Publickey: key3},
			{Coinbase: common.HexToAddress("0xd985e9871d1be109af5a7f6407b1d6b686901fff"), Publickey: key4},
			{Coinbase: common.HexToAddress("0x7c357530174275dd30e46319b89f71186256e4f7"), Publickey: key5},
			{Coinbase: common.HexToAddress("0x4cf807958b9f6d9fd9331397d7a89a079ef43288"), Publickey: key6},
			{Coinbase: common.HexToAddress("0x04d2252a3e0ca7c2aa81247ca33060855a34a808"), Publickey: key7},
		},
	}
}

// DefaultTestnetGenesisBlock returns the Ropsten network genesis block.
func DefaultTestnetGenesisBlock() *Genesis {
	seedkey1 := hexutil.MustDecode("0x042afba5a6680b5361bb57761ca67a7ea309d2883bda93c5d9521078258bb97b03610002865fb27993fcea4918023144eb516706ea33c7c94fef7b2f330cb9d0a6")
	seedkey2 := hexutil.MustDecode("0x04e444bc40b6d1372a955fb9bb9a986ceb1c13a450794151fbf48033189351f6bddddcbebfa5c6d205887551e9527e6deff2cbee9f233ffe14fd15db4beb9c9f34")
	seedkey3 := hexutil.MustDecode("0x049620df839696f4451842fd543b38d171f7f215dcd2c7fcd813c0206f097206a67b25ad719fbb62570c4a4ba467ec61aa396788e3ae79c704a62ea759beca3175")
	seedkey4 := hexutil.MustDecode("0x04f714bb815a9ecc505eae7e756b63753850df92a0fe4c99dc8b6660ba17bbcbb88000d9efb524eb38746ef4505ad2ab1895efccbcc966d4c685c811bda7c9d8ef")

	seedkey5 := hexutil.MustDecode("0x04c0617eef5000dc4a48fb4483735a33c7b2e58e3301fec13b55e9369f8b2bd04c59d899a1fe977b06a3db71fd7c8036b564ffa07171071835a7bb9e24cff22312")
	seedkey6 := hexutil.MustDecode("0x0420bf209047d5eace814848692360a83065841ee91445a8b71b6092f681bf7741a5497ae0a28c401cda133ba8d12ca3dbc6ae756d2fc55288abc159c2ddf601fc")
	seedkey7 := hexutil.MustDecode("0x043736280e96284f5d9460fd874f2dbe6b82ae29d7f348b931f540cc7612f41f20319c76ac90f3de8c68db2e9c7cf9bdfe0fca62046b0f35d01404d49d1de2a43e")
	seedkey8 := hexutil.MustDecode("0x042896914b006d756bd5536069cf99d99e6dd7c8efb5dca582c44b6be293701f1c3a70f1d38de52e4180618fc9b9fbf1896ef445e7f3e51160a8b0e4ed5dc7823b")
	seedkey9 := hexutil.MustDecode("0x04b548e8a1180c649efe64db740dce38417a8fde8a77bf659cf485489d8e608032f71c96cb6988fa3e55927b43a7d70572599be8792c446bdd6261114632767b44")
	seedkey10 := hexutil.MustDecode("0x0440fae92a40624911932dfc31cfb93c2ba4a865ec8b640f15b7886daf2a2d93ad697a310d521af8552130305a00c96d7a27aad990b24264d7637a81ed46836a52")
	seedkey11 := hexutil.MustDecode("0x043177df05ba2ad027e3a1f657002b8530206c07c747f86c28b5a9d9a7b11680bd03ee22710b6013446e9925fa82a3a72de396b4839a81b2cb5fc93fd1ee6f5a78")
	seedkey12 := hexutil.MustDecode("0x040eed64a645c75e8436bc3680eb89db0592d4e95bbf865127b712d4840529864094a0b1c7a6389a33c0e03bc1633e7ed160235ba77a654a6a781b68ed8500ab1c")
	seedkey13 := hexutil.MustDecode("0x04f61cdcd76e0a52f299378b53f182a5e135ea8cca327c11a2fbcc475daf3f0be858a3bda72352cb49d27dc28520ca6f708a39b0f12c49595beb76b3eec253959d")
	seedkey14 := hexutil.MustDecode("0x048be0f382ee382517c2858f7d3abd2421469aac42bc04a6e964ed71e718c58e4178b2e8d1e0671885b66eb7ccfa432e0ea4958d08ce5f18d8e77e7dcf5191cfd5")

	coinbase := common.HexToAddress("0x0000000000000000000000000000000000000000")
	// amount, _ := new(big.Int).SetString("90000000000000000000000", 10)
	return &Genesis{
		Config:     params.TestnetChainConfig,
		Nonce:      928,
		ExtraData:  hexutil.MustDecode("0x54727565436861696E20546573744E6574203033"),
		GasLimit:   20971520,
		Difficulty: big.NewInt(6000000),
		Timestamp:  1537891200,
		Coinbase:   common.HexToAddress("0x0000000000000000000000000000000000000000"),
		Mixhash:    common.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000000"),
		ParentHash: common.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000000"),
		Alloc:      map[common.Address]types.GenesisAccount{
			// common.HexToAddress("0x7c357530174275dd30e46319b89f71186256e4f7"): {Balance: amount},
			// common.HexToAddress("0x4cf807958b9f6d9fd9331397d7a89a079ef43288"): {Balance: amount},
		},
		Committee: []*types.CommitteeMember{
			&types.CommitteeMember{Coinbase: coinbase, Publickey: seedkey1},
			&types.CommitteeMember{Coinbase: coinbase, Publickey: seedkey2},
			&types.CommitteeMember{Coinbase: coinbase, Publickey: seedkey3},
			&types.CommitteeMember{Coinbase: coinbase, Publickey: seedkey4},
			&types.CommitteeMember{Coinbase: coinbase, Publickey: seedkey5},
			&types.CommitteeMember{Coinbase: coinbase, Publickey: seedkey6},
			&types.CommitteeMember{Coinbase: coinbase, Publickey: seedkey7},
			&types.CommitteeMember{Coinbase: coinbase, Publickey: seedkey8},
			&types.CommitteeMember{Coinbase: coinbase, Publickey: seedkey9},
			&types.CommitteeMember{Coinbase: coinbase, Publickey: seedkey10},
			&types.CommitteeMember{Coinbase: coinbase, Publickey: seedkey11},
			&types.CommitteeMember{Coinbase: coinbase, Publickey: seedkey12},
			&types.CommitteeMember{Coinbase: coinbase, Publickey: seedkey13},
			&types.CommitteeMember{Coinbase: coinbase, Publickey: seedkey14},
		},
	}
}
