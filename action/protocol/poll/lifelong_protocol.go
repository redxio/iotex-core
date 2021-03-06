// Copyright (c) 2019 IoTeX Foundation
// This is an alpha (internal) release and is not suitable for production. This source code is provided 'as is' and no
// warranties are given as to title or non-infringement, merchantability or fitness for purpose and, to the extent
// permitted by law, all liability for your use of the code is disclaimed. This source code is governed by Apache
// License 2.0 that can be found in the LICENSE file.

package poll

import (
	"context"

	"github.com/iotexproject/go-pkgs/hash"
	"github.com/iotexproject/iotex-address/address"
	"github.com/pkg/errors"
	"go.uber.org/zap"

	"github.com/iotexproject/iotex-core/action"
	"github.com/iotexproject/iotex-core/action/protocol"
	"github.com/iotexproject/iotex-core/action/protocol/rolldpos"
	"github.com/iotexproject/iotex-core/blockchain/genesis"
	"github.com/iotexproject/iotex-core/crypto"
	"github.com/iotexproject/iotex-core/pkg/log"
	"github.com/iotexproject/iotex-core/state"
)

type lifeLongDelegatesProtocol struct {
	delegates state.CandidateList
	addr      address.Address
}

// NewLifeLongDelegatesProtocol creates a poll protocol with life long delegates
func NewLifeLongDelegatesProtocol(delegates []genesis.Delegate) Protocol {
	var l state.CandidateList
	for _, delegate := range delegates {
		rewardAddress := delegate.RewardAddr()
		if rewardAddress == nil {
			rewardAddress = delegate.OperatorAddr()
		}
		l = append(l, &state.Candidate{
			Address: delegate.OperatorAddr().String(),
			// TODO: load votes from genesis
			Votes:         delegate.Votes(),
			RewardAddress: rewardAddress.String(),
		})
	}
	h := hash.Hash160b([]byte(protocolID))
	addr, err := address.FromBytes(h[:])
	if err != nil {
		log.L().Panic("Error when constructing the address of poll protocol", zap.Error(err))
	}
	return &lifeLongDelegatesProtocol{delegates: l, addr: addr}
}

func (p *lifeLongDelegatesProtocol) CreateGenesisStates(
	ctx context.Context,
	sm protocol.StateManager,
) (err error) {
	blkCtx := protocol.MustGetBlockCtx(ctx)
	if blkCtx.BlockHeight != 0 {
		return errors.Errorf("Cannot create genesis state for height %d", blkCtx.BlockHeight)
	}
	log.L().Info("Creating genesis states for lifelong delegates protocol")
	return setCandidates(ctx, sm, p.delegates, uint64(1))
}

func (p *lifeLongDelegatesProtocol) Handle(ctx context.Context, act action.Action, sm protocol.StateManager) (*action.Receipt, error) {
	return handle(ctx, act, sm, p.addr.String())
}

func (p *lifeLongDelegatesProtocol) Validate(ctx context.Context, act action.Action) error {
	return validate(ctx, p, act)
}

func (p *lifeLongDelegatesProtocol) CalculateCandidatesByHeight(ctx context.Context, height uint64) (state.CandidateList, error) {
	return p.delegates, nil
}

func (p *lifeLongDelegatesProtocol) DelegatesByEpoch(ctx context.Context, epochNum uint64) (state.CandidateList, error) {
	bcCtx := protocol.MustGetBlockchainCtx(ctx)
	rp := rolldpos.MustGetProtocol(bcCtx.Registry)
	tipEpochNum := rp.GetEpochNum(bcCtx.Tip.Height)
	if tipEpochNum+1 == epochNum {
		return p.readActiveBlockProducersByEpoch(ctx, epochNum, true)
	} else if tipEpochNum == epochNum {
		return p.readActiveBlockProducersByEpoch(ctx, epochNum, false)
	}
	return nil, errors.Errorf("wrong epochNumber to get delegates, epochNumber %d can't be less than tip epoch number %d", epochNum, tipEpochNum)
}

func (p *lifeLongDelegatesProtocol) CandidatesByHeight(ctx context.Context, height uint64) (state.CandidateList, error) {
	return p.delegates, nil
}

func (p *lifeLongDelegatesProtocol) ReadState(
	ctx context.Context,
	sr protocol.StateReader,
	method []byte,
	args ...[]byte,
) ([]byte, error) {
	switch string(method) {
	case "CandidatesByEpoch":
		fallthrough
	case "BlockProducersByEpoch":
		fallthrough
	case "ActiveBlockProducersByEpoch":
		return p.readBlockProducers()
	case "GetGravityChainStartHeight":
		if len(args) != 1 {
			return nil, errors.Errorf("invalid number of arguments %d", len(args))
		}
		return args[0], nil
	default:
		return nil, errors.New("corresponding method isn't found")
	}
}

// Register registers the protocol with a unique ID
func (p *lifeLongDelegatesProtocol) Register(r *protocol.Registry) error {
	return r.Register(protocolID, p)
}

// ForceRegister registers the protocol with a unique ID and force replacing the previous protocol if it exists
func (p *lifeLongDelegatesProtocol) ForceRegister(r *protocol.Registry) error {
	return r.ForceRegister(protocolID, p)
}

func (p *lifeLongDelegatesProtocol) readBlockProducers() ([]byte, error) {
	return p.delegates.Serialize()
}

func (p *lifeLongDelegatesProtocol) readActiveBlockProducersByEpoch(ctx context.Context, epochNum uint64, _ bool) (state.CandidateList, error) {
	var blockProducerList []string
	bcCtx := protocol.MustGetBlockchainCtx(ctx)
	rp := rolldpos.MustGetProtocol(bcCtx.Registry)
	blockProducerMap := make(map[string]*state.Candidate)
	delegates := p.delegates
	if len(p.delegates) > int(rp.NumCandidateDelegates()) {
		delegates = p.delegates[:rp.NumCandidateDelegates()]
	}
	for _, bp := range delegates {
		blockProducerList = append(blockProducerList, bp.Address)
		blockProducerMap[bp.Address] = bp
	}

	epochHeight := rp.GetEpochHeight(epochNum)
	crypto.SortCandidates(blockProducerList, epochHeight, crypto.CryptoSeed)
	// TODO: kick-out unqualified delegates based on productivity
	length := int(rp.NumDelegates())
	if len(blockProducerList) < length {
		// TODO: if the number of delegates is smaller than expected, should it return error or not?
		length = len(blockProducerList)
	}

	var activeBlockProducers state.CandidateList
	for i := 0; i < length; i++ {
		activeBlockProducers = append(activeBlockProducers, blockProducerMap[blockProducerList[i]])
	}
	return activeBlockProducers, nil
}
