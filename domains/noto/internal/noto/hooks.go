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

package noto

import (
	"github.com/hyperledger/firefly-signer/pkg/abi"
	"github.com/kaleido-io/paladin/domains/noto/pkg/types"
	"github.com/kaleido-io/paladin/toolkit/pkg/tktypes"
)

type MintHookParams struct {
	Sender   *tktypes.EthAddress `json:"sender"`
	To       *tktypes.EthAddress `json:"to"`
	Amount   *tktypes.HexUint256 `json:"amount"`
	Prepared PreparedTransaction `json:"prepared"`
}

type TransferHookParams struct {
	Sender   *tktypes.EthAddress `json:"sender"`
	From     *tktypes.EthAddress `json:"from"`
	To       *tktypes.EthAddress `json:"to"`
	Amount   *tktypes.HexUint256 `json:"amount"`
	Prepared PreparedTransaction `json:"prepared"`
}

type BurnHookParams struct {
	Sender   *tktypes.EthAddress `json:"sender"`
	From     *tktypes.EthAddress `json:"from"`
	Amount   *tktypes.HexUint256 `json:"amount"`
	Prepared PreparedTransaction `json:"prepared"`
}

type ApproveTransferHookParams struct {
	Sender   *tktypes.EthAddress `json:"sender"`
	From     *tktypes.EthAddress `json:"from"`
	Delegate *tktypes.EthAddress `json:"delegate"`
	Prepared PreparedTransaction `json:"prepared"`
}

type LockHookParams struct {
	Sender     *tktypes.EthAddress   `json:"sender"`
	ID         tktypes.Bytes32       `json:"id"`
	From       *tktypes.EthAddress   `json:"from"`
	Amount     *tktypes.HexUint256   `json:"amount"`
	Recipients []*tktypes.EthAddress `json:"recipients"`
	Prepared   PreparedTransaction   `json:"prepared"`
}

type UnlockHookParams struct {
	ID        tktypes.Bytes32     `json:"id"`
	Recipient *tktypes.EthAddress `json:"recipient"`
}

type PreparedTransaction struct {
	ContractAddress *tktypes.EthAddress `json:"contractAddress"`
	EncodedCall     tktypes.HexBytes    `json:"encodedCall"`
}

func penteInvokeABI(name string, inputs abi.ParameterArray) *abi.Entry {
	return &abi.Entry{
		Name: name,
		Type: "function",
		Inputs: abi.ParameterArray{
			{
				Name:         "group",
				Type:         "tuple",
				InternalType: "struct Group",
				Components: abi.ParameterArray{
					{Name: "salt", Type: "bytes32"},
					{Name: "members", Type: "string[]"},
				},
			},
			{Name: "to", Type: "address"},
			{
				Name:         "inputs",
				Type:         "tuple",
				InternalType: "struct PrivateInvokeInputs",
				Components:   inputs,
			},
		},
		Outputs: abi.ParameterArray{},
	}
}

type PenteInvokeParams struct {
	Group  *types.PentePrivateGroup `json:"group"`
	To     *tktypes.EthAddress      `json:"to"`
	Inputs any                      `json:"inputs"`
}
