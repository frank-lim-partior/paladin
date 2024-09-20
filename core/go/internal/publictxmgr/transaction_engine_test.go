/*
 * Copyright © 2024 Kaleido, Inc.
 *
 * Licensed under the Apache License, Version 2.0 (the "License"); you may not use this file except in compliance with
 * the License. You may obtain a copy of the License at
 *
 * http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software distributed under the License is distributed on
 * an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied. See the License for the
 * specific language governing permissions and limitations under the License.
 *
 * SPDX-License-Identifier: Apache-2.0
 */

package publictxmgr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hyperledger/firefly-common/pkg/config"
	"github.com/hyperledger/firefly-signer/pkg/abi"
	"github.com/hyperledger/firefly-signer/pkg/ethsigner"
	"github.com/hyperledger/firefly-signer/pkg/ethtypes"
	"github.com/kaleido-io/paladin/core/internal/components"
	"github.com/kaleido-io/paladin/core/mocks/componentmocks"
	"github.com/kaleido-io/paladin/core/pkg/blockindexer"
	"github.com/kaleido-io/paladin/toolkit/pkg/algorithms"
	"github.com/kaleido-io/paladin/toolkit/pkg/confutil"
	"github.com/kaleido-io/paladin/toolkit/pkg/tktypes"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

const testMainSigningAddress = testDestAddress

func NewTestTransactionEngine(t *testing.T) (*publicTxEngine, config.Section) {
	ctx := context.Background()
	conf := config.RootSection("unittest")
	InitConfig(conf)
	engineConf := conf.SubSection(TransactionEngineSection)
	engineConf.Set(TransactionEngineIntervalDurationString, "1h")
	engineConf.Set(TransactionEngineMaxInFlightOrchestratorsInt, -1)
	engConf := conf.SubSection(OrchestratorSection)
	engConf.Set(OrchestratorIntervalDurationString, "1h")
	engConf.Set(OrchestratorMaxInFlightTransactionsInt, -1)
	engConf.Set(OrchestratorSubmissionRetryCountInt, 0)

	th, err := NewTransactionEngine(ctx, conf)
	assert.Nil(t, err)
	return th.(*publicTxEngine), conf
}

func TestNewEngineErrors(t *testing.T) {
	ctx := context.Background()

	conf := config.RootSection("unittest")
	InitConfig(conf)

	// gasPriceIncreaseMax parsing error
	orchestratorConf := conf.SubSection(OrchestratorSection)
	orchestratorConf.Set(OrchestratorGasPriceIncreaseMaxBigIntString, "not a big int")
	_, err := NewTransactionEngine(ctx, conf)
	assert.NotNil(t, err)
	assert.Regexp(t, "PD011909", err)
	orchestratorConf.Set(OrchestratorGasPriceIncreaseMaxBigIntString, "")

	orchestratorConf.Set(OrchestratorGasPriceIncreaseMaxBigIntString, "1")
	h, err := NewTransactionEngine(ctx, conf)
	ble := h.(*publicTxEngine)
	require.NoError(t, err)
	assert.Equal(t, big.NewInt(1), ble.gasPriceIncreaseMax)
	orchestratorConf.Set(OrchestratorGasPriceIncreaseMaxBigIntString, "")
}

func TestInit(t *testing.T) {
	ctx := context.Background()
	ble, _ := NewTestTransactionEngine(t)

	mTS := componentmocks.NewPublicTransactionStore(t)
	mBI := componentmocks.NewBlockIndexer(t)
	mEN := componentmocks.NewPublicTxEventNotifier(t)
	mEC := componentmocks.NewEthClient(t)
	mKM := componentmocks.NewKeyManager(t)
	listed := make(chan struct{})
	mBI.On("RegisterIndexedTransactionHandler", ctx, mock.Anything).Return(nil).Once()
	mTS.On("ListTransactions", mock.Anything, mock.Anything).Return([]*components.PublicTX{}, nil).Run(func(args mock.Arguments) {
		listed <- struct{}{}
	}).Once()
	ble.gasPriceClient = NewTestFixedPriceGasPriceClient(t)
	ble.Init(ctx, mEC, mKM, mTS, mEN, mBI)
	ble.enginePollingInterval = 1 * time.Hour
	ble.maxInFlightOrchestrators = 1
	// starts ok
	_, _ = ble.Start(ctx)
	<-listed
	// init errors
	afConfig := ble.balanceManagerConfig.SubSection(BalanceManagerAutoFuelingSection)
	afConfig.Set(BalanceManagerAutoFuelingSourceAddressMinimumBalanceBigIntString, "not a big int")
	assert.Panics(t, func() {
		ble.Init(ctx, mEC, mKM, mTS, mEN, mBI)
	})
	afConfig.Set(BalanceManagerAutoFuelingSourceAddressMinimumBalanceBigIntString, "0")
}

func TestInitFailedRegisterIndexedTransactionHandler(t *testing.T) {
	ctx := context.Background()
	ble, _ := NewTestTransactionEngine(t)

	mTS := componentmocks.NewPublicTransactionStore(t)
	mBI := componentmocks.NewBlockIndexer(t)
	mEN := componentmocks.NewPublicTxEventNotifier(t)
	mEC := componentmocks.NewEthClient(t)
	mKM := componentmocks.NewKeyManager(t)
	mBI.On("RegisterIndexedTransactionHandler", ctx, mock.Anything).Return(errors.New("pop")).Once()

	ble.gasPriceClient = NewTestFixedPriceGasPriceClient(t)
	ble.Init(ctx, mEC, mKM, mTS, mEN, mBI)
	ble.enginePollingInterval = 1 * time.Hour
	ble.maxInFlightOrchestrators = 1
	// start error
	_, initErr := ble.Start(ctx)
	assert.Regexp(t, "pop", initErr)

}

func TestHandleNewTransactionForTransferOnly(t *testing.T) {
	ctx := context.Background()

	ble, _ := NewTestTransactionEngine(t)
	ble.gasPriceClient = NewTestFixedPriceGasPriceClient(t)
	mTS := componentmocks.NewPublicTransactionStore(t)
	mBI := componentmocks.NewBlockIndexer(t)
	mEN := componentmocks.NewPublicTxEventNotifier(t)
	mEC := componentmocks.NewEthClient(t)
	mKM := componentmocks.NewKeyManager(t)
	ble.Init(ctx, mEC, mKM, mTS, mEN, mBI)
	testEthTxInput := &ethsigner.Transaction{
		From:  []byte(testAutoFuelingSourceAddress),
		To:    ethtypes.MustNewAddress(testDestAddress),
		Value: ethtypes.NewHexInteger64(100),
	}
	txID := uuid.New()

	// resolve key failure
	mKM.On("ResolveKey", ctx, testAutoFuelingSourceAddress, algorithms.ECDSA_SECP256K1_PLAINBYTES).Return("", "", fmt.Errorf("pop")).Once()
	_, submissionRejected, err := ble.HandleNewTransaction(ctx, &components.RequestOptions{
		ID:       &txID,
		SignerID: string(testEthTxInput.From),
	}, &components.EthTransfer{
		To:    *tktypes.MustEthAddress(testEthTxInput.To.String()),
		Value: testEthTxInput.Value,
	})
	assert.NotNil(t, err)
	assert.False(t, submissionRejected)
	assert.Regexp(t, "pop", err)

	mKM.On("ResolveKey", ctx, testAutoFuelingSourceAddress, algorithms.ECDSA_SECP256K1_PLAINBYTES).Return("", testAutoFuelingSourceAddress, nil)
	// estimation failure - for non-revert
	mEC.On("GasEstimate", mock.Anything, testEthTxInput, mock.Anything).Return(nil, fmt.Errorf("GasEstimate error")).Once()
	_, submissionRejected, err = ble.HandleNewTransaction(ctx, &components.RequestOptions{
		ID:       &txID,
		SignerID: string(testEthTxInput.From),
	}, &components.EthTransfer{
		To:    *tktypes.MustEthAddress(testEthTxInput.To.String()),
		Value: testEthTxInput.Value,
	})
	assert.NotNil(t, err)
	assert.False(t, submissionRejected)
	assert.Regexp(t, "GasEstimate error", err)

	// estimation failure - for revert
	txID = uuid.New()
	mEC.On("GasEstimate", mock.Anything, testEthTxInput, mock.Anything).Return(nil, fmt.Errorf("execution reverted")).Once()
	_, submissionRejected, err = ble.HandleNewTransaction(ctx, &components.RequestOptions{
		ID:       &txID,
		SignerID: string(testEthTxInput.From),
	}, &components.EthTransfer{
		To:    *tktypes.MustEthAddress(testEthTxInput.To.String()),
		Value: testEthTxInput.Value,
	})
	assert.NotNil(t, err)
	assert.True(t, submissionRejected)
	assert.Regexp(t, "execution reverted", err)
	// insert transaction next nonce error
	mEC.On("GasEstimate", mock.Anything, testEthTxInput, mock.Anything).Return(ethtypes.NewHexInteger(big.NewInt(10)), nil)
	mEC.On("GetTransactionCount", mock.Anything, mock.Anything).
		Return(nil, fmt.Errorf("pop")).Once().Once()
	_, submissionRejected, err = ble.HandleNewTransaction(ctx, &components.RequestOptions{
		ID:       &txID,
		SignerID: string(testEthTxInput.From),
	}, &components.EthTransfer{
		To:    *tktypes.MustEthAddress(testEthTxInput.To.String()),
		Value: testEthTxInput.Value,
	})
	assert.NotNil(t, err)
	assert.Regexp(t, "pop", err)
	assert.False(t, submissionRejected)
	// create transaction succeeded
	// gas estimate should be cached
	mTS.On("InsertTransaction", ctx, mock.Anything, mock.Anything).Return(nil).Once()
	mEC.On("GetTransactionCount", mock.Anything, mock.Anything).
		Return(confutil.P(ethtypes.HexUint64(1)), nil).Once()
	mTS.On("UpdateSubStatus", ctx, txID.String(), components.PubTxSubStatusReceived, components.BaseTxActionAssignNonce, mock.Anything, mock.Anything, mock.Anything).Return(nil)

	_, _, err = ble.HandleNewTransaction(ctx, &components.RequestOptions{
		ID:       &txID,
		SignerID: string(testEthTxInput.From),
	}, &components.EthTransfer{
		To:    *tktypes.MustEthAddress(testEthTxInput.To.String()),
		Value: testEthTxInput.Value,
	})
	require.NoError(t, err)
	mEC.AssertNotCalled(t, "GasEstimate")
}

func TestHandleNewTransactionTransferOnlyWithProvideGas(t *testing.T) {
	ctx := context.Background()
	ble, _ := NewTestTransactionEngine(t)
	mTS := componentmocks.NewPublicTransactionStore(t)
	mBI := componentmocks.NewBlockIndexer(t)
	mEN := componentmocks.NewPublicTxEventNotifier(t)
	mEC := componentmocks.NewEthClient(t)
	mKM := componentmocks.NewKeyManager(t)
	mKM.On("ResolveKey", ctx, testAutoFuelingSourceAddress, algorithms.ECDSA_SECP256K1_PLAINBYTES).Return("", testAutoFuelingSourceAddress, nil)
	// fall back to connector when get call failed
	ble.gasPriceClient = NewTestNodeGasPriceClient(t, mEC)
	ble.Init(ctx, mEC, mKM, mTS, mEN, mBI)
	testEthTxInput := &ethsigner.Transaction{
		From:     []byte(testAutoFuelingSourceAddress),
		To:       ethtypes.MustNewAddress(testDestAddress),
		GasLimit: ethtypes.NewHexInteger64(1223451),
		Value:    ethtypes.NewHexInteger64(100),
	}
	// create transaction succeeded
	// gas estimate should be cached
	insertMock := mTS.On("InsertTransaction", ctx, mock.Anything, mock.Anything)
	mEC.On("GetTransactionCount", mock.Anything, mock.Anything).
		Return(confutil.P(ethtypes.HexUint64(1)), nil).Once()
	insertMock.Run(func(args mock.Arguments) {
		mtx := args[2].(*components.PublicTX)
		assert.Equal(t, "1223451", mtx.GasLimit.BigInt().String())
		assert.Nil(t, mtx.GasPrice)
		insertMock.Return(nil)
	}).Once()
	txID := uuid.New()
	mTS.On("UpdateSubStatus", ctx, txID.String(), components.PubTxSubStatusReceived, components.BaseTxActionAssignNonce, mock.Anything, mock.Anything, mock.Anything).Return(nil)

	_, _, err := ble.HandleNewTransaction(ctx, &components.RequestOptions{
		ID:       &txID,
		SignerID: string(testEthTxInput.From),
		GasLimit: testEthTxInput.GasLimit,
	}, &components.EthTransfer{
		To:    *tktypes.MustEthAddress(testEthTxInput.To.String()),
		Value: testEthTxInput.Value,
	})
	require.NoError(t, err)
	mEC.AssertNotCalled(t, "GasEstimate")
}

func TestHandleNewTransactionTransferAndInvalidType(t *testing.T) {
	ctx := context.Background()
	ble, _ := NewTestTransactionEngine(t)
	ble.gasPriceClient = NewTestZeroGasPriceChainClient(t)
	mTS := componentmocks.NewPublicTransactionStore(t)
	mBI := componentmocks.NewBlockIndexer(t)
	mEN := componentmocks.NewPublicTxEventNotifier(t)
	mEC := componentmocks.NewEthClient(t)
	mKM := componentmocks.NewKeyManager(t)
	mKM.On("ResolveKey", ctx, testAutoFuelingSourceAddress, algorithms.ECDSA_SECP256K1_PLAINBYTES).Return("", testAutoFuelingSourceAddress, nil)
	ble.Init(ctx, mEC, mKM, mTS, mEN, mBI)
	testEthTxInput := &ethsigner.Transaction{
		From:     []byte(testAutoFuelingSourceAddress),
		To:       ethtypes.MustNewAddress(testDestAddress),
		GasLimit: ethtypes.NewHexInteger64(1223451),
		Value:    ethtypes.NewHexInteger64(100),
	}
	// create transaction succeeded
	// gas estimate should be cached
	insertMock := mTS.On("InsertTransaction", ctx, mock.Anything, mock.Anything)
	mEC.On("GetTransactionCount", mock.Anything, mock.Anything).
		Return(confutil.P(ethtypes.HexUint64(1)), nil).Once()
	insertMock.Run(func(args mock.Arguments) {
		mtx := args[2].(*components.PublicTX)
		assert.Equal(t, "1223451", mtx.GasLimit.BigInt().String())
		assert.Equal(t, "0", mtx.GasPrice.BigInt().String())
		insertMock.Return(nil)
	}).Once()
	txID := uuid.New()
	mTS.On("UpdateSubStatus", ctx, txID.String(), components.PubTxSubStatusReceived, components.BaseTxActionAssignNonce, mock.Anything, mock.Anything, mock.Anything).Return(nil)

	_, _, err := ble.HandleNewTransaction(ctx, &components.RequestOptions{
		ID:       &txID,
		SignerID: string(testEthTxInput.From),
		GasLimit: testEthTxInput.GasLimit,
	}, &components.EthTransfer{
		To:    *tktypes.MustEthAddress(testEthTxInput.To.String()),
		Value: testEthTxInput.Value,
	})
	require.NoError(t, err)
	mEC.AssertNotCalled(t, "GasEstimate")

	_, submissionRejected, err := ble.HandleNewTransaction(ctx, &components.RequestOptions{
		ID:       &txID,
		SignerID: string(testEthTxInput.From),
		GasLimit: testEthTxInput.GasLimit,
	}, "not a valid object")
	assert.Regexp(t, "PD011929", err)
	assert.True(t, submissionRejected)
	mEC.AssertNotCalled(t, "GasEstimate")
}

func TestHandleNewTransaction(t *testing.T) {
	ctx := context.Background()
	ble, _ := NewTestTransactionEngine(t)
	ble.gasPriceClient = NewTestFixedPriceGasPriceClient(t)
	mTS := componentmocks.NewPublicTransactionStore(t)
	mBI := componentmocks.NewBlockIndexer(t)
	mEN := componentmocks.NewPublicTxEventNotifier(t)
	mEC := componentmocks.NewEthClient(t)
	mKM := componentmocks.NewKeyManager(t)
	mKM.On("ResolveKey", ctx, testDestAddress, algorithms.ECDSA_SECP256K1_PLAINBYTES).Return("", testDestAddress, nil)
	ble.Init(ctx, mEC, mKM, mTS, mEN, mBI)
	testEthTxInput := &ethsigner.Transaction{
		From:  []byte(testMainSigningAddress),
		To:    ethtypes.MustNewAddress(testDestAddress),
		Value: ethtypes.NewHexInteger64(100),
		Data:  ethtypes.MustNewHexBytes0xPrefix(""),
	}
	// missing transaction ID
	_, submissionRejected, err := ble.HandleNewTransaction(ctx, &components.RequestOptions{
		SignerID: string(testEthTxInput.From),
		GasLimit: testEthTxInput.GasLimit,
	}, &components.EthTransaction{
		To:          *tktypes.MustEthAddress(testEthTxInput.To.String()),
		FunctionABI: &abi.Entry{},
		Inputs:      &abi.ComponentValue{},
	})
	assert.NotNil(t, err)
	assert.True(t, submissionRejected)
	assert.Regexp(t, "PD011910", err)

	txID := uuid.New()
	// Parse API failure
	mEC.On("ABIFunction", ctx, mock.Anything).Return(nil, fmt.Errorf("ABI function parsing error")).Once()
	_, submissionRejected, err = ble.HandleNewTransaction(ctx, &components.RequestOptions{
		ID:       &txID,
		SignerID: string(testEthTxInput.From),
		GasLimit: testEthTxInput.GasLimit,
	}, &components.EthTransaction{
		To:          *tktypes.MustEthAddress(testEthTxInput.To.String()),
		FunctionABI: nil,
		Inputs:      nil,
	})
	assert.NotNil(t, err)
	assert.False(t, submissionRejected)
	assert.Regexp(t, "ABI function parsing error", err)

	// Build call data failure
	mABIBuilder := componentmocks.NewABIFunctionRequestBuilder(t)
	mABIBuilder.On("BuildCallData").Return(fmt.Errorf("Build data error")).Once()
	mABIF := componentmocks.NewABIFunctionClient(t)
	mABIF.On("R", ctx).Return(mABIBuilder).Once()
	mABIBuilder.On("To", ethtypes.MustNewAddress(testEthTxInput.To.String())).Return(mABIBuilder).Once()
	mABIBuilder.On("Input", mock.Anything).Return(mABIBuilder).Once()
	mEC.On("ABIFunction", ctx, mock.Anything).Return(mABIF, nil).Once()
	_, submissionRejected, err = ble.HandleNewTransaction(ctx, &components.RequestOptions{
		ID:       &txID,
		SignerID: string(testEthTxInput.From),
		GasLimit: testEthTxInput.GasLimit,
	}, &components.EthTransaction{
		To:          *tktypes.MustEthAddress(testEthTxInput.To.String()),
		FunctionABI: nil,
		Inputs:      nil,
	})
	assert.NotNil(t, err)
	assert.False(t, submissionRejected)
	assert.Regexp(t, "Build data error", err)

	// Gas estimate failure - non-revert
	mEC.On("GasEstimate", mock.Anything, testEthTxInput, mock.Anything).Return(nil, fmt.Errorf("something else")).Once()
	mABIBuilder.On("BuildCallData").Return(nil).Once()
	mABIF.On("R", ctx).Return(mABIBuilder).Once()
	mABIBuilder.On("To", ethtypes.MustNewAddress(testEthTxInput.To.String())).Return(mABIBuilder).Once()
	mABIBuilder.On("Input", mock.Anything).Return(mABIBuilder).Once()
	mABIBuilder.On("TX", mock.Anything).Return(testEthTxInput).Once()
	mEC.On("ABIFunction", ctx, mock.Anything).Return(mABIF, nil).Once()
	_, submissionRejected, err = ble.HandleNewTransaction(ctx, &components.RequestOptions{
		ID:       &txID,
		SignerID: string(testEthTxInput.From),
		GasLimit: testEthTxInput.GasLimit,
	}, &components.EthTransaction{
		To:          *tktypes.MustEthAddress(testEthTxInput.To.String()),
		FunctionABI: nil,
		Inputs:      nil,
	})
	assert.NotNil(t, err)
	assert.False(t, submissionRejected)
	assert.Regexp(t, "something else", err)

	// Gas estimate failure - revert
	mEC.On("GasEstimate", mock.Anything, testEthTxInput, mock.Anything).Return(nil, fmt.Errorf("execution reverted")).Once()
	mABIBuilder.On("BuildCallData").Return(nil).Once()
	mABIF.On("R", ctx).Return(mABIBuilder).Once()
	mABIBuilder.On("To", ethtypes.MustNewAddress(testEthTxInput.To.String())).Return(mABIBuilder).Once()
	mABIBuilder.On("Input", mock.Anything).Return(mABIBuilder).Once()
	mABIBuilder.On("TX", mock.Anything).Return(testEthTxInput).Once()
	mEC.On("ABIFunction", ctx, mock.Anything).Return(mABIF, nil).Once()
	_, submissionRejected, err = ble.HandleNewTransaction(ctx, &components.RequestOptions{
		ID:       &txID,
		SignerID: string(testEthTxInput.From),
		GasLimit: testEthTxInput.GasLimit,
	}, &components.EthTransaction{
		To:          *tktypes.MustEthAddress(testEthTxInput.To.String()),
		FunctionABI: nil,
		Inputs:      nil,
	})
	assert.NotNil(t, err)
	assert.True(t, submissionRejected)
	assert.Regexp(t, "execution reverted", err)

	// create transaction succeeded
	mEC.On("GasEstimate", mock.Anything, testEthTxInput, mock.Anything).Return(ethtypes.NewHexInteger64(200), nil).Once()
	mABIBuilder.On("BuildCallData").Return(nil).Once()
	mABIF.On("R", ctx).Return(mABIBuilder).Once()
	mABIBuilder.On("To", ethtypes.MustNewAddress(testEthTxInput.To.String())).Return(mABIBuilder).Once()
	mABIBuilder.On("Input", mock.Anything).Return(mABIBuilder).Once()
	mABIBuilder.On("TX", mock.Anything).Return(testEthTxInput).Once()
	mEC.On("ABIFunction", ctx, mock.Anything).Return(mABIF, nil).Once()
	insertMock := mTS.On("InsertTransaction", ctx, mock.Anything, mock.Anything)
	mEC.On("GetTransactionCount", mock.Anything, mock.Anything).
		Return(confutil.P(ethtypes.HexUint64(1)), nil).Once()
	insertMock.Run(func(args mock.Arguments) {
		mtx := args[2].(*components.PublicTX)
		assert.Equal(t, big.NewInt(200), mtx.GasLimit.BigInt())
		insertMock.Return(nil)
	}).Once()
	mTS.On("UpdateSubStatus", ctx, txID.String(), components.PubTxSubStatusReceived, components.BaseTxActionAssignNonce, mock.Anything, mock.Anything, mock.Anything).Return(nil)
	_, _, err = ble.HandleNewTransaction(ctx, &components.RequestOptions{
		ID:       &txID,
		SignerID: string(testEthTxInput.From),
		GasLimit: testEthTxInput.GasLimit,
	}, &components.EthTransaction{
		To:          *tktypes.MustEthAddress(testEthTxInput.To.String()),
		FunctionABI: nil,
		Inputs:      nil,
	})
	require.NoError(t, err)
}

func TestHandleNewDeployment(t *testing.T) {
	ctx := context.Background()
	ble, _ := NewTestTransactionEngine(t)
	ble.gasPriceClient = NewTestFixedPriceGasPriceClient(t)
	mTS := componentmocks.NewPublicTransactionStore(t)
	mBI := componentmocks.NewBlockIndexer(t)
	mEN := componentmocks.NewPublicTxEventNotifier(t)
	mEC := componentmocks.NewEthClient(t)
	mKM := componentmocks.NewKeyManager(t)
	mKM.On("ResolveKey", ctx, testDestAddress, algorithms.ECDSA_SECP256K1_PLAINBYTES).Return("", testDestAddress, nil)
	ble.Init(ctx, mEC, mKM, mTS, mEN, mBI)
	testEthTxInput := &ethsigner.Transaction{
		From: []byte(testMainSigningAddress),
		Data: ethtypes.MustNewHexBytes0xPrefix(""),
	}
	txID := uuid.New()
	// Parse API failure
	mEC.On("ABIConstructor", ctx, mock.Anything, mock.Anything).Return(nil, fmt.Errorf("ABI function parsing error")).Once()
	_, submissionRejected, err := ble.HandleNewTransaction(ctx, &components.RequestOptions{
		ID:       &txID,
		SignerID: string(testEthTxInput.From),
		GasLimit: testEthTxInput.GasLimit,
	}, &components.EthDeployTransaction{
		ConstructorABI: nil,
		Bytecode:       nil,
		Inputs:         nil,
	})
	assert.NotNil(t, err)
	assert.False(t, submissionRejected)
	assert.Regexp(t, "ABI function parsing error", err)

	// Build call data failure
	mABIBuilder := componentmocks.NewABIFunctionRequestBuilder(t)
	mABIBuilder.On("BuildCallData").Return(fmt.Errorf("Build data error")).Once()
	mABIF := componentmocks.NewABIFunctionClient(t)
	mABIF.On("R", ctx).Return(mABIBuilder).Once()
	mABIBuilder.On("Input", mock.Anything).Return(mABIBuilder).Once()
	mEC.On("ABIConstructor", ctx, mock.Anything, mock.Anything).Return(mABIF, nil).Once()
	_, submissionRejected, err = ble.HandleNewTransaction(ctx, &components.RequestOptions{
		ID:       &txID,
		SignerID: string(testEthTxInput.From),
		GasLimit: testEthTxInput.GasLimit,
	}, &components.EthDeployTransaction{
		ConstructorABI: nil,
		Bytecode:       tktypes.HexBytes(testTransactionData),
		Inputs:         nil,
	})
	assert.NotNil(t, err)
	assert.False(t, submissionRejected)
	assert.Regexp(t, "Build data error", err)

	// Gas estimate failure - non-revert
	mEC.On("GasEstimate", mock.Anything, testEthTxInput, mock.Anything).Return(nil, fmt.Errorf("something else")).Once()
	mABIBuilder.On("BuildCallData").Return(nil).Once()
	mABIF.On("R", ctx).Return(mABIBuilder).Once()
	mABIBuilder.On("Input", mock.Anything).Return(mABIBuilder).Once()
	mABIBuilder.On("TX", mock.Anything).Return(testEthTxInput).Once()
	mEC.On("ABIConstructor", ctx, mock.Anything, mock.Anything).Return(mABIF, nil).Once()
	_, submissionRejected, err = ble.HandleNewTransaction(ctx, &components.RequestOptions{
		ID:       &txID,
		SignerID: string(testEthTxInput.From),
		GasLimit: testEthTxInput.GasLimit,
	}, &components.EthDeployTransaction{
		ConstructorABI: nil,
		Bytecode:       tktypes.HexBytes(testTransactionData),
		Inputs:         nil,
	})
	assert.NotNil(t, err)
	assert.False(t, submissionRejected)
	assert.Regexp(t, "something else", err)

	// Gas estimate failure - revert
	mEC.On("GasEstimate", mock.Anything, testEthTxInput, mock.Anything).Return(nil, fmt.Errorf("execution reverted")).Once()
	mABIBuilder.On("BuildCallData").Return(nil).Once()
	mABIF.On("R", ctx).Return(mABIBuilder).Once()
	mABIBuilder.On("Input", mock.Anything).Return(mABIBuilder).Once()
	mABIBuilder.On("TX", mock.Anything).Return(testEthTxInput).Once()
	mEC.On("ABIConstructor", ctx, mock.Anything, mock.Anything).Return(mABIF, nil).Once()
	_, submissionRejected, err = ble.HandleNewTransaction(ctx, &components.RequestOptions{
		ID:       &txID,
		SignerID: string(testEthTxInput.From),
		GasLimit: testEthTxInput.GasLimit,
	}, &components.EthDeployTransaction{
		ConstructorABI: nil,
		Bytecode:       tktypes.HexBytes(testTransactionData),
		Inputs:         nil,
	})
	assert.NotNil(t, err)
	assert.True(t, submissionRejected)
	assert.Regexp(t, "execution reverted", err)

	// create transaction succeeded
	mEC.On("GasEstimate", mock.Anything, testEthTxInput, mock.Anything).Return(ethtypes.NewHexInteger64(200), nil).Once()
	mABIBuilder.On("BuildCallData").Return(nil).Once()
	mABIF.On("R", ctx).Return(mABIBuilder).Once()
	mABIBuilder.On("Input", mock.Anything).Return(mABIBuilder).Once()
	mABIBuilder.On("TX", mock.Anything).Return(testEthTxInput).Once()
	mEC.On("ABIConstructor", ctx, mock.Anything, mock.Anything).Return(mABIF, nil).Once()
	insertMock := mTS.On("InsertTransaction", ctx, mock.Anything, mock.Anything)
	mEC.On("GetTransactionCount", mock.Anything, mock.Anything).
		Return(confutil.P(ethtypes.HexUint64(1)), nil).Once()
	insertMock.Run(func(args mock.Arguments) {
		mtx := args[2].(*components.PublicTX)
		assert.Equal(t, big.NewInt(200), mtx.GasLimit.BigInt())
		insertMock.Return(nil)
	}).Once()
	mTS.On("UpdateSubStatus", ctx, txID.String(), components.PubTxSubStatusReceived, components.BaseTxActionAssignNonce, mock.Anything, mock.Anything, mock.Anything).Return(nil)
	_, _, err = ble.HandleNewTransaction(ctx, &components.RequestOptions{
		ID:       &txID,
		SignerID: string(testEthTxInput.From),
		GasLimit: testEthTxInput.GasLimit,
	}, &components.EthDeployTransaction{
		ConstructorABI: nil,
		Bytecode:       tktypes.HexBytes(testTransactionData),
		Inputs:         nil,
	})
	require.NoError(t, err)
}

func TestEngineSuspend(t *testing.T) {
	ctx := context.Background()
	ble, _ := NewTestTransactionEngine(t)
	ble.gasPriceClient = NewTestFixedPriceGasPriceClient(t)

	mTS := componentmocks.NewPublicTransactionStore(t)
	mBI := componentmocks.NewBlockIndexer(t)
	mEN := componentmocks.NewPublicTxEventNotifier(t)

	mEC := componentmocks.NewEthClient(t)
	mKM := componentmocks.NewKeyManager(t)
	ble.Init(ctx, mEC, mKM, mTS, mEN, mBI)

	imtxs := NewTestInMemoryTxState(t)
	mtx := imtxs.GetTx()

	// errored
	mTS.On("GetTransactionByID", ctx, mtx.ID.String()).Return(nil, fmt.Errorf("get error")).Once()
	_, err := ble.HandleSuspendTransaction(ctx, mtx.ID.String())
	assert.Regexp(t, "get error", err)

	// engine update error
	suspendedStatus := components.PubTxStatusSuspended
	mTS.On("GetTransactionByID", ctx, mtx.ID.String()).Return(mtx, nil).Once()
	mTS.On("UpdateTransaction", ctx, mtx.ID.String(), &components.BaseTXUpdates{
		Status: &suspendedStatus,
	}).Return(fmt.Errorf("update error")).Once()
	_, err = ble.HandleSuspendTransaction(ctx, mtx.ID.String())
	assert.Regexp(t, "update error", err)

	// engine update success
	mtx.Status = components.PubTxStatusPending
	mTS.On("GetTransactionByID", ctx, mtx.ID.String()).Return(mtx, nil).Once()
	mTS.On("UpdateTransaction", ctx, mtx.ID.String(), &components.BaseTXUpdates{
		Status: &suspendedStatus,
	}).Return(nil).Once()
	tx, err := ble.HandleSuspendTransaction(ctx, mtx.ID.String())
	require.NoError(t, err)
	assert.Equal(t, suspendedStatus, tx.Status)

	// orchestrator handler tests
	ble.InFlightOrchestrators = make(map[string]*orchestrator)
	ble.InFlightOrchestrators[string(mtx.From)] = &orchestrator{
		publicTxEngine:               ble,
		orchestratorPollingInterval:  ble.enginePollingInterval,
		state:                        OrchestratorStateIdle,
		stateEntryTime:               time.Now().Add(-ble.maxOrchestratorIdle).Add(-1 * time.Minute),
		InFlightTxsStale:             make(chan bool, 1),
		stopProcess:                  make(chan bool, 1),
		transactionIDsInStatusUpdate: []string{"randomID"},
		txStore:                      mTS,
		ethClient:                    mEC,
		publicTXEventNotifier:        mEN,
		bIndexer:                     mBI,
	}
	// orchestrator update error
	mTS.On("GetTransactionByID", ctx, mtx.ID.String()).Return(mtx, nil).Once()
	mTS.On("UpdateTransaction", ctx, mtx.ID.String(), &components.BaseTXUpdates{
		Status: &suspendedStatus,
	}).Return(fmt.Errorf("update error")).Once()
	_, err = ble.HandleSuspendTransaction(ctx, mtx.ID.String())
	assert.Regexp(t, "update error", err)

	// orchestrator update success
	mtx.Status = components.PubTxStatusPending
	mTS.On("GetTransactionByID", ctx, mtx.ID.String()).Return(mtx, nil).Once()
	mTS.On("UpdateTransaction", ctx, mtx.ID.String(), &components.BaseTXUpdates{
		Status: &suspendedStatus,
	}).Return(nil).Once()
	tx, err = ble.HandleSuspendTransaction(ctx, mtx.ID.String())
	require.NoError(t, err)
	assert.Equal(t, suspendedStatus, tx.Status)

	// in flight tx test
	testInFlightTransactionStateManagerWithMocks := NewTestInFlightTransactionWithMocks(t)
	it := testInFlightTransactionStateManagerWithMocks.it
	mtx = it.stateManager.GetTx()
	ble.InFlightOrchestrators[string(mtx.From)].InFlightTxs = []*InFlightTransactionStageController{
		it,
	}

	// async status update queued
	mtx.Status = components.PubTxStatusPending
	mTS.On("GetTransactionByID", ctx, mtx.ID.String()).Return(mtx, nil).Once()
	tx, err = ble.HandleSuspendTransaction(ctx, mtx.ID.String())
	require.NoError(t, err)
	assert.Equal(t, components.PubTxStatusPending, tx.Status)

	// already on the target status
	mtx.Status = components.PubTxStatusSuspended
	mTS.On("GetTransactionByID", ctx, mtx.ID.String()).Return(mtx, nil).Once()
	tx, err = ble.HandleSuspendTransaction(ctx, mtx.ID.String())
	require.NoError(t, err)
	assert.Equal(t, components.PubTxStatusSuspended, tx.Status)

	// error when try to update the status of a completed tx
	mtx.Status = components.PubTxStatusFailed
	mTS.On("GetTransactionByID", ctx, mtx.ID.String()).Return(mtx, nil).Once()
	_, err = ble.HandleSuspendTransaction(ctx, mtx.ID.String())
	assert.Regexp(t, "PD011921", err)
}

func TestEngineResume(t *testing.T) {
	ctx := context.Background()

	ble, _ := NewTestTransactionEngine(t)
	ble.gasPriceClient = NewTestFixedPriceGasPriceClient(t)

	mTS := componentmocks.NewPublicTransactionStore(t)
	mBI := componentmocks.NewBlockIndexer(t)
	mEN := componentmocks.NewPublicTxEventNotifier(t)

	mEC := componentmocks.NewEthClient(t)
	mKM := componentmocks.NewKeyManager(t)
	ble.Init(ctx, mEC, mKM, mTS, mEN, mBI)

	imtxs := NewTestInMemoryTxState(t)
	mtx := imtxs.GetTx()

	// errored
	mTS.On("GetTransactionByID", ctx, mtx.ID.String()).Return(nil, fmt.Errorf("get error")).Once()
	_, err := ble.HandleResumeTransaction(ctx, mtx.ID.String())
	assert.Regexp(t, "get error", err)

	// engine update error
	pendingStatus := components.PubTxStatusPending
	mTS.On("GetTransactionByID", ctx, mtx.ID.String()).Return(mtx, nil).Once()
	mTS.On("UpdateTransaction", ctx, mtx.ID.String(), &components.BaseTXUpdates{
		Status: &pendingStatus,
	}).Return(fmt.Errorf("update error")).Once()
	_, err = ble.HandleResumeTransaction(ctx, mtx.ID.String())
	assert.Regexp(t, "update error", err)

	// engine update success
	mtx.Status = components.PubTxStatusSuspended
	mTS.On("GetTransactionByID", ctx, mtx.ID.String()).Return(mtx, nil).Once()
	mTS.On("UpdateTransaction", ctx, mtx.ID.String(), &components.BaseTXUpdates{
		Status: &pendingStatus,
	}).Return(nil).Once()
	tx, err := ble.HandleResumeTransaction(ctx, mtx.ID.String())
	require.NoError(t, err)
	assert.Equal(t, pendingStatus, tx.Status)

	// orchestrator handler tests
	ble.InFlightOrchestrators = make(map[string]*orchestrator)
	ble.InFlightOrchestrators[string(mtx.From)] = &orchestrator{
		publicTxEngine:               ble,
		orchestratorPollingInterval:  ble.enginePollingInterval,
		state:                        OrchestratorStateIdle,
		stateEntryTime:               time.Now().Add(-ble.maxOrchestratorIdle).Add(-1 * time.Minute),
		InFlightTxsStale:             make(chan bool, 1),
		stopProcess:                  make(chan bool, 1),
		transactionIDsInStatusUpdate: []string{"randomID"},
		txStore:                      mTS,
		ethClient:                    mEC,
		publicTXEventNotifier:        mEN,
		bIndexer:                     mBI,
	}
	// orchestrator update error
	mTS.On("GetTransactionByID", ctx, mtx.ID.String()).Return(mtx, nil).Once()
	mTS.On("UpdateTransaction", ctx, mtx.ID.String(), &components.BaseTXUpdates{
		Status: &pendingStatus,
	}).Return(fmt.Errorf("update error")).Once()
	_, err = ble.HandleResumeTransaction(ctx, mtx.ID.String())
	assert.Regexp(t, "update error", err)

	// orchestrator update success
	mtx.Status = components.PubTxStatusSuspended
	mTS.On("GetTransactionByID", ctx, mtx.ID.String()).Return(mtx, nil).Once()
	mTS.On("UpdateTransaction", ctx, mtx.ID.String(), &components.BaseTXUpdates{
		Status: &pendingStatus,
	}).Return(nil).Once()
	tx, err = ble.HandleResumeTransaction(ctx, mtx.ID.String())
	require.NoError(t, err)
	assert.Equal(t, pendingStatus, tx.Status)

	// in flight tx test
	testInFlightTransactionStateManagerWithMocks := NewTestInFlightTransactionWithMocks(t)
	it := testInFlightTransactionStateManagerWithMocks.it
	mtx = it.stateManager.GetTx()
	ble.InFlightOrchestrators[string(mtx.From)].InFlightTxs = []*InFlightTransactionStageController{
		it,
	}

	// async status update queued
	mtx.Status = components.PubTxStatusSuspended
	mTS.On("GetTransactionByID", ctx, mtx.ID.String()).Return(mtx, nil).Once()
	tx, err = ble.HandleResumeTransaction(ctx, mtx.ID.String())
	require.NoError(t, err)
	assert.Equal(t, components.PubTxStatusSuspended, tx.Status)

	// already on the target status
	mtx.Status = components.PubTxStatusPending
	mTS.On("GetTransactionByID", ctx, mtx.ID.String()).Return(mtx, nil).Once()
	tx, err = ble.HandleResumeTransaction(ctx, mtx.ID.String())
	require.NoError(t, err)
	assert.Equal(t, components.PubTxStatusPending, tx.Status)

	// error when try to update the status of a completed tx
	mtx.Status = components.PubTxStatusFailed
	mTS.On("GetTransactionByID", ctx, mtx.ID.String()).Return(mtx, nil).Once()
	_, err = ble.HandleResumeTransaction(ctx, mtx.ID.String())
	assert.Regexp(t, "PD011921", err)
}

func TestEngineCanceledContext(t *testing.T) {
	ctx, cancelCtx := context.WithCancel(context.Background())

	ble, _ := NewTestTransactionEngine(t)
	ble.gasPriceClient = NewTestFixedPriceGasPriceClient(t)

	mTS := componentmocks.NewPublicTransactionStore(t)
	mBI := componentmocks.NewBlockIndexer(t)
	mEN := componentmocks.NewPublicTxEventNotifier(t)

	mEC := componentmocks.NewEthClient(t)
	mKM := componentmocks.NewKeyManager(t)
	ble.Init(ctx, mEC, mKM, mTS, mEN, mBI)

	imtxs := NewTestInMemoryTxState(t)
	mtx := imtxs.GetTx()
	mTS.On("UpdateTransaction", ctx, mtx.ID.String(), mock.Anything).Return(nil).Maybe()

	// Suspend
	mTS.On("GetTransactionByID", ctx, mtx.ID.String()).Return(mtx, nil).Run(func(args mock.Arguments) {
		cancelCtx()
	}).Once()
	_, err := ble.HandleSuspendTransaction(ctx, mtx.ID.String())
	assert.Regexp(t, "PD011926", err)

	// Resume
	mTS.On("GetTransactionByID", ctx, mtx.ID.String()).Return(mtx, nil).Run(func(args mock.Arguments) {
		cancelCtx()
	}).Once()
	_, err = ble.HandleResumeTransaction(ctx, mtx.ID.String())
	assert.Regexp(t, "PD011926", err)
}

func TestEngineHandleConfirmedTransactionEvents(t *testing.T) {
	ctx, cancelCtx := context.WithCancel(context.Background())

	ble, _ := NewTestTransactionEngine(t)
	ble.gasPriceClient = NewTestFixedPriceGasPriceClient(t)

	mTS := componentmocks.NewPublicTransactionStore(t)
	mBI := componentmocks.NewBlockIndexer(t)
	mEN := componentmocks.NewPublicTxEventNotifier(t)

	mEC := componentmocks.NewEthClient(t)
	mKM := componentmocks.NewKeyManager(t)
	ble.Init(ctx, mEC, mKM, mTS, mEN, mBI)

	imtxs := NewTestInMemoryTxState(t)
	mtx := imtxs.GetTx()

	mockManagedTx0 := &components.PublicTX{
		Transaction: &ethsigner.Transaction{
			From:  json.RawMessage(string(mtx.From)),
			Nonce: ethtypes.NewHexInteger64(4),
		},
	}
	mockManagedTx1 := &components.PublicTX{
		Transaction: &ethsigner.Transaction{
			From:  json.RawMessage(string(mtx.From)),
			Nonce: ethtypes.NewHexInteger64(5),
		},
	}
	mockManagedTx2 := &components.PublicTX{
		Transaction: &ethsigner.Transaction{
			From:  json.RawMessage("0x12345f6e918321dd47c86e7a077b4ab0e7411234"),
			Nonce: ethtypes.NewHexInteger64(6),
		},
	}
	mockManagedTx3 := &components.PublicTX{
		Transaction: &ethsigner.Transaction{
			From:  json.RawMessage("0x43215f6e918321dd47c86e7a077b4ab0e7414321"),
			Nonce: ethtypes.NewHexInteger64(7),
		},
	}

	ble.InFlightOrchestrators = make(map[string]*orchestrator)
	ble.InFlightOrchestrators[string(mtx.From)] = &orchestrator{
		publicTxEngine:               ble,
		orchestratorPollingInterval:  ble.enginePollingInterval,
		state:                        OrchestratorStateIdle,
		stateEntryTime:               time.Now().Add(-ble.maxOrchestratorIdle).Add(-1 * time.Minute),
		InFlightTxsStale:             make(chan bool, 1),
		stopProcess:                  make(chan bool, 1),
		transactionIDsInStatusUpdate: []string{"randomID"},
		txStore:                      mTS,
		ethClient:                    mEC,
		publicTXEventNotifier:        mEN,
		bIndexer:                     mBI,
	}
	// in flight tx test
	testInFlightTransactionStateManagerWithMocks := NewTestInFlightTransactionWithMocks(t)
	it := testInFlightTransactionStateManagerWithMocks.it
	mtx = it.stateManager.GetTx()
	ble.InFlightOrchestrators[string(mtx.From)].InFlightTxs = []*InFlightTransactionStageController{
		it,
	}
	ble.maxInFlightOrchestrators = 2
	ble.ctx = ctx

	assert.Equal(t, 1, len(ble.InFlightOrchestrators))
	err := ble.HandleConfirmedTransactions(ctx, []*blockindexer.IndexedTransaction{
		{
			BlockNumber:      int64(1233),
			TransactionIndex: int64(23),
			Hash:             tktypes.Bytes32Keccak([]byte("0x00001")),
			Result:           blockindexer.TXResult_SUCCESS.Enum(),
			Nonce:            mtx.Nonce.Uint64(),
			From:             tktypes.MustEthAddress(string(mtx.From)),
		},
		{
			BlockNumber:      int64(1233),
			TransactionIndex: int64(23),
			Hash:             tktypes.Bytes32Keccak([]byte("0x00002")),
			Result:           blockindexer.TXResult_SUCCESS.Enum(),
			Nonce:            mockManagedTx0.Nonce.Uint64(),
			From:             tktypes.MustEthAddress(string(mockManagedTx0.From)),
		},
		{
			BlockNumber:      int64(1233),
			TransactionIndex: int64(23),
			Hash:             tktypes.Bytes32Keccak([]byte("0x00002")),
			Result:           blockindexer.TXResult_SUCCESS.Enum(),
			Nonce:            mockManagedTx1.Nonce.Uint64(),
			From:             tktypes.MustEthAddress(string(mockManagedTx1.From)),
		},
		{
			BlockNumber:      int64(1233),
			TransactionIndex: int64(23),
			Hash:             tktypes.Bytes32Keccak([]byte("0x00002")),
			Result:           blockindexer.TXResult_SUCCESS.Enum(),
			Nonce:            mockManagedTx2.Nonce.Uint64(),
			From:             tktypes.MustEthAddress(string(mockManagedTx2.From)),
		},
		{
			BlockNumber:      int64(1233),
			TransactionIndex: int64(23),
			Hash:             tktypes.Bytes32Keccak([]byte("0x00002")),
			Result:           blockindexer.TXResult_SUCCESS.Enum(),
			Nonce:            mockManagedTx3.Nonce.Uint64(),
			From:             tktypes.MustEthAddress(string(mockManagedTx3.From)),
		},
	})
	assert.NoError(t, err)
	assert.Equal(t, 2, len(ble.InFlightOrchestrators))

	// cancel context should return with error
	cancelCtx()
	assert.Regexp(t, "PD010301", ble.HandleConfirmedTransactions(ctx, []*blockindexer.IndexedTransaction{
		{
			BlockNumber:      int64(1233),
			TransactionIndex: int64(23),
			Hash:             tktypes.Bytes32Keccak([]byte("0x00001")),
			Result:           blockindexer.TXResult_SUCCESS.Enum(),
			Nonce:            mtx.Nonce.Uint64(),
			From:             tktypes.MustEthAddress(string(mtx.From)),
		},
	}))
}

func TestEngineHandleConfirmedTransactionEventsNoInFlightNotHang(t *testing.T) {
	ctx := context.Background()

	ble, _ := NewTestTransactionEngine(t)
	ble.gasPriceClient = NewTestFixedPriceGasPriceClient(t)

	mTS := componentmocks.NewPublicTransactionStore(t)
	mBI := componentmocks.NewBlockIndexer(t)
	mEN := componentmocks.NewPublicTxEventNotifier(t)

	mEC := componentmocks.NewEthClient(t)
	mKM := componentmocks.NewKeyManager(t)
	ble.Init(ctx, mEC, mKM, mTS, mEN, mBI)

	ble.InFlightOrchestrators = map[string]*orchestrator{}
	// test not hang
	assert.NoError(t, ble.HandleConfirmedTransactions(ctx, []*blockindexer.IndexedTransaction{}))
}
