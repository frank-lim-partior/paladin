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
	internal "github.com/kaleido-io/paladin/domains/noto/internal/noto"
	"github.com/kaleido-io/paladin/domains/noto/pkg/types"
	"github.com/kaleido-io/paladin/toolkit/pkg/plugintk"
)

type Noto interface {
	plugintk.DomainAPI
	GetHandler(method string) types.DomainHandler
	Name() string
	CoinSchemaID() string
	LockedCoinSchemaID() string
	LockInfoSchemaID() string
}

func New(callbacks plugintk.DomainCallbacks) Noto {
	return &internal.Noto{Callbacks: callbacks}
}
